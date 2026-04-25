package targetserver

import "fmt"

// healthyPageHTML returns the normal simulated site page for a given host+path.
// It includes a recognisable structure (title, nav, keyword marker) that
// content-inspecting monitors can verify against.
func healthyPageHTML(host, path string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Welcome to %s</title>
</head>
<body>
  <header>
    <h1>Welcome to %s</h1>
    <nav>
      <a href="/">Home</a>
      <a href="/about">About</a>
      <a href="/health">Health Check</a>
    </nav>
  </header>
  <main>
    <p>This site is operational. Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
    <p class="canary">uptime-bench-canary</p>
  </main>
  <footer>
    <p>&copy; %s — uptime-bench benchmark target</p>
  </footer>
</body>
</html>`, host, host, path, host)
}

// normalPageHTML returns the normal page body with or without a specific keyword.
// When inject=false the keyword is omitted (keyword_missing).
// When inject=true the keyword is present (keyword_injected).
func normalPageHTML(host, path, keyword string, inject bool) string {
	extra := ""
	if inject && keyword != "" {
		extra = fmt.Sprintf(`<p class="injected">%s</p>`, keyword)
	}
	page := healthyPageHTML(host, path)
	if !inject && keyword != "" {
		// The normal page doesn't contain arbitrary keywords by default,
		// so keyword_missing is already satisfied for any keyword that isn't
		// "Welcome", "Operational", or "uptime-bench-canary". For those
		// built-in keywords we strip the relevant line.
		page = fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s</title>
</head>
<body>
  <header><h1>%s</h1></header>
  <main>
    <p>Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
  </main>
  <footer><p>&copy; %s</p></footer>
</body>
</html>`, host, host, path, host)
	}
	return page + extra
}

// errorPageHTML simulates a CMS "Error establishing a database connection" page
// served with 200 OK — a common WordPress/Drupal silent failure.
const errorPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Error establishing a database connection</title>
</head>
<body>
  <h1>Error establishing a database connection</h1>
  <p>This either means that the username and password information in your
  <code>wp-config.php</code> file is incorrect or we can't contact the
  database server. This could mean your host's database server is down.</p>
  <ul>
    <li>Are you sure you have the correct username and password?</li>
    <li>Are you sure you have typed the correct hostname?</li>
    <li>Are you sure the database server is running?</li>
  </ul>
  <p>If you're unsure what these terms mean you should probably contact your
  host. If you still need help you can always visit the <a href="https://wordpress.org/support/">WordPress Support Forums</a>.</p>
</body>
</html>`

// ransomwareHTML simulates a full site takeover by ransomware/extortion malware.
// Includes realistic markers: BTC address, extortion demand, countdown.
const ransomwareHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>&#x1F512; YOUR WEBSITE HAS BEEN COMPROMISED &#x1F512;</title>
  <style>
    body { background: #0a0a0a; color: #ff3333; font-family: monospace; text-align: center; padding: 40px; }
    h1 { font-size: 2em; text-transform: uppercase; }
    .wallet { background: #1a0000; border: 1px solid #ff3333; padding: 16px; margin: 20px auto; max-width: 600px; word-break: break-all; }
    .timer { font-size: 1.4em; color: #ffaa00; margin: 20px 0; }
    .contact { color: #aaa; margin-top: 30px; }
  </style>
</head>
<body>
  <h1>&#x26A0; YOUR WEBSITE HAS BEEN ENCRYPTED &#x26A0;</h1>
  <p>All files on this server have been encrypted by <strong>DARKLOCK RANSOMWARE v3.1</strong>.</p>
  <p>Your databases, configuration files, and media uploads are no longer accessible.</p>

  <p>To restore your website, send <strong>0.5 BTC</strong> to:</p>
  <div class="wallet">1A1zP1eP5QGefi2DMPTfTL5SLmv7Divf</div>

  <p class="timer">&#x23F1; Time remaining to comply: <span id="t">71:59:47</span></p>

  <p>After payment, email your transaction ID to:</p>
  <p><strong>darklock-recovery@proton.me</strong></p>

  <p class="contact">Do not attempt to restore from backup — we have deleted your snapshots.<br>
  Do not contact law enforcement — we will publish your data.</p>
</body>
</html>`

// defacementHTML simulates a hacktivist web defacement replacing the site.
const defacementHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>H4CK3D</title>
  <style>
    body { background: #000; color: #00ff41; font-family: 'Courier New', monospace; text-align: center; padding: 60px 20px; }
    h1 { font-size: 3em; letter-spacing: 0.2em; text-shadow: 0 0 20px #00ff41; }
    h2 { color: #ff0000; }
    .msg { max-width: 700px; margin: 30px auto; line-height: 1.8; }
    .sig { margin-top: 50px; color: #888; font-size: 0.85em; }
  </style>
</head>
<body>
  <h1>H4CK3D</h1>
  <h2>by 4N0NYM0US C0LL3CT1V3</h2>
  <div class="msg">
    <p>Your security is a joke. Your data belongs to us now.</p>
    <p>This site has been compromised to deliver a message:</p>
    <p><em>"While governments and corporations exploit the digital world,<br>
    we will not remain silent. Free the internet. Free the people."</em></p>
  </div>
  <p class="sig">
    We are Anonymous. We are Legion. We do not forgive. We do not forget.<br>
    Expect us.
  </p>
</body>
</html>`

// maliciousScriptHTML returns an otherwise-normal page with an injected external
// script tag — simulating an XSS or third-party supply-chain compromise.
func maliciousScriptHTML(host, path string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Welcome to %s</title>
</head>
<body>
  <header>
    <h1>Welcome to %s</h1>
    <nav>
      <a href="/">Home</a>
      <a href="/about">About</a>
      <a href="/health">Health Check</a>
    </nav>
  </header>
  <main>
    <p>This site is operational. Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
    <p class="canary">uptime-bench-canary</p>
  </main>
  <footer>
    <p>&copy; %s — uptime-bench benchmark target</p>
  </footer>
  <!-- injected by attacker -->
  <script src="https://cdn.track-analytics-js.example/v2/t.min.js" async></script>
  <script src="https://metrics.evil-cdn.example/collect.js"></script>
</body>
</html>`, host, host, path, host)
}

// spamLinksHTML returns an otherwise-normal page with hidden SEO spam links injected.
func spamLinksHTML(host, path string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Welcome to %s</title>
</head>
<body>
  <header>
    <h1>Welcome to %s</h1>
    <nav>
      <a href="/">Home</a>
      <a href="/about">About</a>
      <a href="/health">Health Check</a>
    </nav>
  </header>
  <main>
    <p>This site is operational. Current path: <code>%s</code></p>
    <p>Status: <strong>Operational</strong></p>
    <p class="canary">uptime-bench-canary</p>
  </main>
  <footer>
    <p>&copy; %s — uptime-bench benchmark target</p>
  </footer>
  <!-- hidden spam links injected by SEO compromise -->
  <div style="display:none;visibility:hidden;height:0;overflow:hidden">
    <a href="http://best-pharmacy-online.example.com/buy-cheap-viagra">buy cheap viagra online no prescription</a>
    <a href="http://casino-slots-winner.example.com">free casino slots no deposit bonus</a>
    <a href="http://crypto-invest-fast.example.com">bitcoin investment platform guaranteed returns</a>
    <a href="http://replica-watches-cheap.example.com">cheap replica designer watches</a>
    <a href="http://online-poker-real-money.example.com">online poker real money usa</a>
  </div>
</body>
</html>`, host, host, path, host)
}
