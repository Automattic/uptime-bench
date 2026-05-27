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

const wpMissingMySQLExtensionHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>WordPress Error</title></head>
<body>
  <h1>Your PHP installation appears to be missing the MySQL extension which is required by WordPress.</h1>
  <p>Please check that the mysqli PHP extension is installed and enabled.</p>
</body>
</html>`

const wpPHPFatalHTML = `<!DOCTYPE html>
<html><head><title>Fatal error</title></head><body>
<br />
<b>Fatal error</b>: Uncaught Error: Call to undefined function broken_plugin_boot() in <b>/var/www/html/wp-content/plugins/broken/plugin.php</b> on line <b>42</b><br />
</body></html>`

const wpAllowedMemoryHTML = `<!DOCTYPE html>
<html><head><title>Fatal error</title></head><body>
<br />
<b>Fatal error</b>: Allowed memory size of 134217728 bytes exhausted in <b>/srv/www/example.com/public_html/wp-includes/functions.php</b> on line <b>1287</b><br />
</body></html>`

const wpMaxExecutionHTML = `<!DOCTYPE html>
<html><head><title>Fatal error</title></head><body>
<br />
<b>Fatal error</b>: Maximum execution time of 30 seconds exceeded in <b>/home/example/public_html/wp-content/themes/example/functions.php</b> on line <b>91</b><br />
</body></html>`

const wpParseErrorHTML = `<!DOCTYPE html>
<html><head><title>Parse error</title></head><body>
<br />
<b>Parse error</b>: syntax error, unexpected token "}" in <b>/var/www/html/wp-content/plugins/broken/plugin.php</b> on line <b>17</b><br />
</body></html>`

const wpSetupConfigHTML = `<!DOCTYPE html>
<html><head><title>WordPress &rsaquo; Setup Configuration File</title></head><body>
  <h1>Welcome to WordPress. Before getting started</h1>
  <p>We need some information on the database. You will need to know the following items before proceeding.</p>
</body></html>`

const wpDBRepairHTML = `<!DOCTYPE html>
<html><head><title>Database Repair Required</title></head><body>
  <h1>One or more database tables are unavailable. The database may need to be repaired.</h1>
  <p>To allow use of this page to automatically repair database problems, please add the following line to your wp-config.php file.</p>
</body></html>`

const wpDBMissingTablesHTML = `<!DOCTYPE html>
<html><head><title>WordPress Database Error</title></head><body>
  <h1>Database tables are missing</h1>
  <p>WordPress could not find required database tables such as <code>wp_options</code> and <code>wp_posts</code>.</p>
  <p>Please check your <code>wp-config.php</code> database settings and restore the missing WordPress tables.</p>
</body></html>`

const wpDBTableCrashedHTML = `<!DOCTYPE html>
<html><head><title>WordPress Database Error</title></head><body>
  <h1>WordPress database error</h1>
  <p>WordPress database error: Table './wordpress/wp_options' is marked as crashed and should be repaired for query SELECT option_name, option_value FROM wp_options.</p>
  <p>Automatic repair failed; manual database repair may be required.</p>
</body></html>`

const wpMissingConfigHTML = `<!DOCTYPE html>
<html><head><title>WordPress &rsaquo; Error</title></head><body>
  <h1>There doesn't seem to be a <code>wp-config.php</code> file.</h1>
  <p>I need this before we can get started. Need more help? We got it.</p>
  <p>You can create a <code>wp-config.php</code> file through a web interface, but this doesn't work for all server setups.</p>
</body></html>`

const wpDBUpdateRequiredHTML = `<!DOCTYPE html>
<html><head><title>Database Update Required</title></head><body>
  <h1>Database Update Required</h1>
  <p>WordPress has been updated! Before we send you on your way, we have to update your database to the newest version.</p>
  <p>The database update process may take a little while, so please be patient.</p>
</body></html>`

const wpMaintenanceHTML = `<!DOCTYPE html>
<html><head><title>Briefly unavailable for scheduled maintenance</title></head><body>
  <h1>Briefly unavailable for scheduled maintenance. Check back in a minute.</h1>
  <p>WordPress is currently performing routine maintenance.</p>
</body></html>`

const wpUnsupportedPHPHTML = `<!DOCTYPE html>
<html><head><title>Unsupported PHP Version</title></head><body>
  <h1>Your server is running PHP version 5.6.40 but WordPress requires at least 7.4.0.</h1>
  <p>Please update PHP to continue using WordPress.</p>
</body></html>`

const wpUnsupportedDatabaseHTML = `<!DOCTYPE html>
<html><head><title>Unsupported Database Version</title></head><body>
  <h1>Error: WordPress 6.5.5 requires MySQL 5.7 or higher. You are running 5.5.62.</h1>
  <p>Please contact your hosting provider to upgrade your database server.</p>
</body></html>`

const wpUnsupportedMariaDBHTML = `<!DOCTYPE html>
<html><head><title>Unsupported Database Version</title></head><body>
  <h1>Error: WordPress 6.5.5 requires MariaDB 10.4 or higher. You are running 10.1.48.</h1>
  <p>Please contact your hosting provider to upgrade your database server.</p>
</body></html>`

const wpCriticalThisWebsiteHTML = `<!DOCTYPE html>
<html><head><title>WordPress Error</title></head><body>
  <h1>There has been a critical error on this website.</h1>
  <p>Learn more about troubleshooting WordPress.</p>
</body></html>`

const wpCriticalYourWebsiteHTML = `<!DOCTYPE html>
<html><head><title>WordPress Error</title></head><body>
  <h1>There has been a critical error on your website.</h1>
  <p>Learn more about troubleshooting WordPress.</p>
</body></html>`

const wpTechnicalThisSiteHTML = `<!DOCTYPE html>
<html><head><title>WordPress Error</title></head><body>
  <h1>The site is experiencing technical difficulties.</h1>
  <p>Please check your site admin email inbox for instructions.</p>
</body></html>`

const wpTechnicalTheSiteHTML = `<!DOCTYPE html>
<html><head><title>WordPress Error</title></head><body>
  <h1>The site is experiencing technical difficulties.</h1>
  <p>Please check the site admin email inbox for instructions.</p>
</body></html>`

const apacheDefaultHTML = `<!DOCTYPE html>
<html><head><title>Apache2 Ubuntu Default Page: It works</title></head><body>
  <h1>Apache2 Ubuntu Default Page</h1>
  <p>It works!</p>
</body></html>`

const nginxDefaultHTML = `<!DOCTYPE html>
<html><head><title>Welcome to nginx!</title></head><body>
  <h1>Welcome to nginx!</h1>
  <p>If you see this page, the nginx web server is successfully installed and working.</p>
</body></html>`

const hostingSuspendedHTML = `<!DOCTYPE html>
<html><head><title>This account has been suspended</title></head><body>
  <h1>This account has been suspended</h1>
  <p>Contact your hosting provider for more information.</p>
  <p>Reference: /cgi-sys/suspendedpage.cgi</p>
</body></html>`

const jetpackProbeHTML = `<!DOCTYPE html>
<html><head><title>Jetpack Probe</title></head><body>
  <p>Hi Jetpack! All Systems go.</p>
</body></html>`

const jetpackProbeCompactHTML = `<!DOCTYPE html>
<html><head><title>Jetpack Probe</title></head><body>
  <p>Hi Jetpack!All Systems go</p>
</body></html>`

const xmlrpcEndpointEchoHTML = `<!DOCTYPE html>
<html><head><title>XML-RPC server accepts POST requests only.</title></head><body>
  <p>XML-RPC server accepts POST requests only.</p>
</body></html>`

const wpDirectoryListingHTML = `<!DOCTYPE html>
<html><head><title>Index of /</title></head><body>
  <h1>Index of /</h1>
  <pre>
<a href="wp-admin/">wp-admin/</a>
<a href="wp-content/">wp-content/</a>
<a href="wp-includes/">wp-includes/</a>
  </pre>
  <address>Apache/2.4.58 Server at example.test Port 80</address>
</body></html>`

const healthyFatalArticleHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Debugging PHP fatal error messages</title></head>
<body>
  <article>
    <h1>How to read a fatal error during local development</h1>
    <p>This healthy article discusses the phrase fatal error in general terms for developers.</p>
    <p>It intentionally does not include a WordPress filesystem path or any line-number evidence.</p>
    <p class="canary">uptime-bench-canary</p>
  </article>
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
