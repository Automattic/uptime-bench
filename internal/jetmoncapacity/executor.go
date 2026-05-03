package jetmoncapacity

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
)

// SQLExecutionResult records the outcome of executing rendered lifecycle SQL.
type SQLExecutionResult struct {
	StatementCount int                  `json:"statement_count"`
	Statements     []SQLStatementResult `json:"statements"`
}

// SQLStatementResult records one statement execution.
type SQLStatementResult struct {
	Index        int        `json:"index"`
	Keyword      string     `json:"keyword"`
	RowsAffected *int64     `json:"rows_affected,omitempty"`
	Columns      []string   `json:"columns,omitempty"`
	Rows         [][]string `json:"rows,omitempty"`
}

// ExecuteSQL executes rendered lifecycle SQL on one DB connection.
//
// A single connection is important because v2 reset plans use temporary tables.
func ExecuteSQL(ctx context.Context, dsn string, sqlText string) (SQLExecutionResult, error) {
	if strings.TrimSpace(dsn) == "" {
		return SQLExecutionResult{}, fmt.Errorf("dsn is required")
	}
	statements := SplitSQLStatements(sqlText)
	result := SQLExecutionResult{StatementCount: len(statements)}
	if len(statements) == 0 {
		return result, nil
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return SQLExecutionResult{}, err
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		return SQLExecutionResult{}, err
	}
	defer conn.Close()

	for i, stmt := range statements {
		keyword := statementKeyword(stmt)
		item := SQLStatementResult{Index: i + 1, Keyword: keyword}
		switch keyword {
		case "SELECT", "SHOW", "WITH":
			rows, err := conn.QueryContext(ctx, stmt)
			if err != nil {
				return result, fmt.Errorf("statement %d %s: %w", i+1, keyword, err)
			}
			columns, values, err := readRows(rows)
			if err != nil {
				return result, fmt.Errorf("statement %d %s: %w", i+1, keyword, err)
			}
			item.Columns = columns
			item.Rows = values
		default:
			execResult, err := conn.ExecContext(ctx, stmt)
			if err != nil {
				return result, fmt.Errorf("statement %d %s: %w", i+1, keyword, err)
			}
			if affected, err := execResult.RowsAffected(); err == nil {
				item.RowsAffected = &affected
			}
		}
		result.Statements = append(result.Statements, item)
	}
	return result, nil
}

// SplitSQLStatements splits rendered lifecycle SQL into executable statements.
func SplitSQLStatements(sqlText string) []string {
	var out []string
	var b strings.Builder
	inSingle := false
	inDouble := false
	inBacktick := false
	lineComment := false
	blockComment := false

	for i := 0; i < len(sqlText); i++ {
		c := sqlText[i]
		next := byte(0)
		if i+1 < len(sqlText) {
			next = sqlText[i+1]
		}

		if lineComment {
			b.WriteByte(c)
			if c == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			b.WriteByte(c)
			if c == '*' && next == '/' {
				b.WriteByte(next)
				i++
				blockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(c)
			if c == '\\' && next != 0 {
				b.WriteByte(next)
				i++
				continue
			}
			if c == '\'' {
				if next == '\'' {
					b.WriteByte(next)
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			b.WriteByte(c)
			if c == '\\' && next != 0 {
				b.WriteByte(next)
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		}
		if inBacktick {
			b.WriteByte(c)
			if c == '`' {
				inBacktick = false
			}
			continue
		}

		switch {
		case c == '-' && next == '-' && (i+2 >= len(sqlText) || unicode.IsSpace(rune(sqlText[i+2]))):
			b.WriteByte(c)
			b.WriteByte(next)
			i++
			lineComment = true
		case c == '/' && next == '*':
			b.WriteByte(c)
			b.WriteByte(next)
			i++
			blockComment = true
		case c == '\'':
			b.WriteByte(c)
			inSingle = true
		case c == '"':
			b.WriteByte(c)
			inDouble = true
		case c == '`':
			b.WriteByte(c)
			inBacktick = true
		case c == ';':
			stmt := strings.TrimSpace(b.String())
			if stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

func statementKeyword(stmt string) string {
	trimmed := strings.TrimSpace(stmt)
	for {
		switch {
		case strings.HasPrefix(trimmed, "--"):
			newline := strings.IndexByte(trimmed, '\n')
			if newline == -1 {
				return "COMMENT"
			}
			trimmed = strings.TrimSpace(trimmed[newline+1:])
		case strings.HasPrefix(trimmed, "/*"):
			end := strings.Index(trimmed, "*/")
			if end == -1 {
				return "COMMENT"
			}
			trimmed = strings.TrimSpace(trimmed[end+2:])
		default:
			fields := strings.Fields(trimmed)
			if len(fields) == 0 {
				return "EMPTY"
			}
			return strings.ToUpper(fields[0])
		}
	}
}

func readRows(rows *sql.Rows) ([]string, [][]string, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]string
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		scan := make([]any, len(columns))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(columns))
		for i, value := range values {
			if value.Valid {
				row[i] = value.String
			} else {
				row[i] = "NULL"
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return columns, out, nil
}
