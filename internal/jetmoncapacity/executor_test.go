package jetmoncapacity

import "testing"

func TestSplitSQLStatementsPreservesQuotedSemicolon(t *testing.T) {
	sql := `
-- comment with ; ignored
START TRANSACTION;
INSERT INTO example (name, note) VALUES ('site-1', 'has;semicolon');
UPDATE example SET note = 'it''s ok' WHERE id = 1;
COMMIT;
`
	statements := SplitSQLStatements(sql)
	if len(statements) != 4 {
		t.Fatalf("statements = %d, want 4: %#v", len(statements), statements)
	}
	if got := statementKeyword(statements[0]); got != "START" {
		t.Fatalf("keyword 0 = %q, want START", got)
	}
	if got := statementKeyword(statements[1]); got != "INSERT" {
		t.Fatalf("keyword 1 = %q, want INSERT", got)
	}
	if got := statementKeyword(statements[2]); got != "UPDATE" {
		t.Fatalf("keyword 2 = %q, want UPDATE", got)
	}
	if got := statementKeyword(statements[3]); got != "COMMIT" {
		t.Fatalf("keyword 3 = %q, want COMMIT", got)
	}
}

func TestSplitSQLStatementsHandlesV2SeedPlan(t *testing.T) {
	sqlText, err := RenderSQL(Plan{
		Action: OperationSeed,
		Config: Config{
			Schema:      SchemaV2,
			BlogIDStart: 100,
			Count:       2,
			URLPattern:  "http://site-%d.example.test/",
			BatchSize:   1,
		},
	})
	if err != nil {
		t.Fatalf("RenderSQL: %v", err)
	}
	statements := SplitSQLStatements(sqlText)
	if len(statements) < 8 {
		t.Fatalf("statements = %d, want at least 8", len(statements))
	}
	if got := statementKeyword(statements[0]); got != "START" {
		t.Fatalf("first keyword = %q, want START", got)
	}
	if got := statementKeyword(statements[len(statements)-1]); got != "COMMIT" {
		t.Fatalf("last keyword = %q, want COMMIT", got)
	}
}
