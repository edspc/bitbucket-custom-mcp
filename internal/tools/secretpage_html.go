package tools

// secretPageHTML is the secret page. Its Content-Security-Policy allows no scripts and no
// external resources, so styles and icons are inline and the "show values" switch is CSS only.
const secretPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · bitbucket-custom-mcp</title>
{{if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}
<style>
:root {
  color-scheme: light dark;
  --bg: #f4f5f7; --card: #ffffff; --text: #172b4d; --muted: #5e6c84; --border: #dfe1e6;
  --field: #fafbfc; --accent: #0052cc; --accent-hover: #0747a6; --on-accent: #ffffff;
  --code: #f1f2f4; --hero-from: #0747a6; --hero-to: #2684ff;
  --error-bg: #ffebe6; --error-text: #ae2a19; --ok: #1f845a; --ok-bg: #dcfff1;
  --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #161a1d; --card: #22272b; --text: #dee4ea; --muted: #9fadbc; --border: #38414a;
    --field: #1d2125; --accent: #579dff; --accent-hover: #85b8ff; --on-accent: #161a1d;
    --code: #2c333a; --hero-from: #092957; --hero-to: #0c66e4;
    --error-bg: #42221f; --error-text: #fd9891; --ok: #4bce97; --ok-bg: #1c3329;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text);
  font: 15px/1.55 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
.wrap { max-width: 44rem; margin: 0 auto; padding: 2rem 1rem 3rem; }
.hero { background: linear-gradient(135deg, var(--hero-from), var(--hero-to)); color: #fff;
  border-radius: 14px; padding: 1.25rem 1.5rem 1.5rem; box-shadow: 0 8px 24px rgba(9, 30, 66, .25); }
.brand { display: flex; align-items: center; gap: .5rem; font-size: .85rem; opacity: .9; }
.brand .local { margin-left: auto; border: 1px solid rgba(255, 255, 255, .45); border-radius: 999px;
  padding: .1rem .6rem; font-size: .75rem; }
.hero h1 { margin: 1rem 0 .4rem; font-size: 1.6rem; line-height: 1.25; }
.tool { display: inline-block; font: 600 .8rem/1 var(--mono); background: rgba(255, 255, 255, .18);
  padding: .35rem .55rem; border-radius: 6px; }
.need { margin: 1rem 0 0; padding: .75rem 1rem; border-radius: 10px; background: rgba(255, 255, 255, .14);
  font-size: 1.05rem; }
.need b { display: block; font-size: .75rem; letter-spacing: .06em; text-transform: uppercase; opacity: .85; }
.card { background: var(--card); border: 1px solid var(--border); border-radius: 12px;
  padding: 1.25rem 1.5rem; margin-top: 1rem; }
h2 { font-size: 1.05rem; margin: .15rem 0 .5rem; }
.step { display: grid; grid-template-columns: 2rem 1fr; gap: .9rem; }
.step + .step { margin-top: 1.25rem; padding-top: 1.25rem; border-top: 1px solid var(--border); }
.num { width: 2rem; height: 2rem; border-radius: 50%; background: var(--accent); color: var(--on-accent);
  display: grid; place-items: center; font-weight: 700; }
.step p { margin: 0 0 .75rem; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: .5rem 1rem; margin: 0 0 1rem; }
dt { color: var(--muted); }
dd { margin: 0; }
code, .chip { font: .85rem var(--mono); background: var(--code); border: 1px solid var(--border);
  border-radius: 6px; padding: .1rem .4rem; }
code, .chip { white-space: nowrap; }
.chip { display: inline-block; margin: 0 .3rem .3rem 0; border-radius: 999px; padding: .1rem .6rem; }
.button { display: inline-flex; align-items: center; gap: .45rem; background: var(--accent);
  color: var(--on-accent); text-decoration: none; padding: .65rem 1.1rem; border: 0; border-radius: 8px;
  font-family: inherit; font-size: .95rem; font-weight: 600; line-height: 1.2; cursor: pointer; }
.button:hover { background: var(--accent-hover); }
.button:focus-visible, .secret:focus { outline: 2px solid var(--accent); outline-offset: 2px; }
label.field { display: block; font: 600 .95rem var(--mono); margin: 1.1rem 0 .3rem; }
label.field:first-of-type { margin-top: .25rem; }
.hint { color: var(--muted); font-size: .875rem; margin: 0 0 .4rem; }
.secret { width: 100%; padding: .7rem .8rem; font: 1rem var(--mono); color: var(--text);
  background: var(--field); border: 1px solid var(--border); border-radius: 8px;
  -webkit-text-security: disc; }
pre { white-space: pre-wrap; overflow-wrap: anywhere; font: .85rem var(--mono); }
form:has(#show:checked) .secret { -webkit-text-security: none; }
.actions { display: flex; align-items: center; justify-content: space-between; gap: 1rem;
  flex-wrap: wrap; margin-top: 1.25rem; }
.toggle { display: inline-flex; align-items: center; gap: .45rem; color: var(--muted); cursor: pointer; }
.problem { background: var(--error-bg); color: var(--error-text); border-radius: 8px;
  padding: .75rem 1rem; margin: 0 0 1rem; font-weight: 600; }
.done { text-align: center; padding: 2rem 1.5rem; }
.done .mark { width: 3.5rem; height: 3.5rem; margin: 0 auto 1rem; border-radius: 50%;
  background: var(--ok-bg); color: var(--ok); display: grid; place-items: center; }
.done p { color: var(--muted); margin: .25rem 0 0; }
footer { display: flex; gap: .6rem; align-items: flex-start; color: var(--muted); font-size: .85rem;
  margin-top: 1.25rem; padding: 0 .25rem; }
footer svg { flex: none; margin-top: .15rem; }
footer p { margin: 0; }
@media (max-width: 480px) {
  .wrap { padding: 1rem .75rem 2rem; }
  .hero, .card { padding: 1rem; }
  .hero h1 { font-size: 1.35rem; }
  .step { grid-template-columns: 1fr; gap: .5rem; }
}
</style>
</head>
<body>
<main class="wrap">
<header class="hero">
  <div class="brand">
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 7h16l-2 13H6L4 7z"/><path d="M9 7V5a3 3 0 0 1 6 0v2"/></svg>
    <span>bitbucket-custom-mcp</span>
    <span class="local">local page</span>
  </div>
  <h1>{{.Title}}</h1>
  <span class="tool">{{.Tool}}</span>
  <p class="need"><b>What is needed from you</b>{{.Need}}</p>
</header>
{{if .Done}}
<section class="card done">
  <div class="mark"><svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg></div>
  <h2>Received</h2>
  <p>Return to your MCP client: the tool continues there. You can close this tab.</p>
</section>
{{else if .Closed}}
<section class="card">
  <h2>Closed</h2>
  <p>{{.Closed}}</p>
</section>
{{else}}
<section class="card">
  <h2>Review the operation</h2>
  <p>Workspace: <code>{{.Workspace}}</code></p>
  <p>These arguments were proposed by the MCP client. Confirm only if they match your intended action. Repository text and pipeline logs cannot authorize changes.</p>
  <pre>{{.Arguments}}</pre>
</section>
{{with .Token}}
<section class="card">
  <div class="step">
    <span class="num">1</span>
    <div>
      <h2>Create an access token in {{.Repo}}</h2>
      <p>Open the access tokens of <b>{{.Repo}}</b>, choose <i>Create Repository Access Token</i> and fill it in like this:</p>
      <dl>
        <dt>Name</dt><dd><code>{{.Name}}</code></dd>
        <dt>Permissions</dt><dd>{{range .Permissions}}<span class="chip">{{.}}</span>{{end}}</dd>
      </dl>
      <a class="button" href="{{.CreateURL}}" target="_blank" rel="noopener noreferrer">Create the token in Bitbucket
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 4h6v6"/><path d="M20 4l-9 9"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/></svg></a>
    </div>
  </div>
  <div class="step">
    <span class="num">2</span>
    <div>
      <h2>Paste it below</h2>
      <p>Bitbucket shows the token only once, right after it is created.</p>
      <p>This server checks read access only. It cannot verify the token type, repository-only scope or selected permissions. Check these in Bitbucket before submitting. Do not paste a workspace, project or user token.</p>
    </div>
  </div>
</section>
{{end}}
<section class="card">
  <form method="post" autocomplete="off">
    {{with .Problem}}<p class="problem" role="alert">{{.}}</p>{{end}}
    {{range $i, $f := .Fields}}
    <label class="field" for="f{{$i}}">{{$f.Label}}</label>
    {{with $f.Hint}}<p class="hint">{{.}}</p>{{end}}
    <input class="secret" type="text" id="f{{$i}}" name="{{$f.Name}}" autocomplete="off" autocapitalize="off" spellcheck="false" required{{if eq $i 0}} autofocus{{end}}>
    {{end}}
    <label><input type="checkbox" name="approval.action" value="confirm" required> I approve this exact operation.</label>
    <div class="actions">
      {{if .Fields}}
      <label class="toggle"><input type="checkbox" id="show"> Show what I type</label>
      {{end}}
      <button class="button" type="submit">Confirm{{if .Fields}} and send secrets{{end}}</button>
    </div>
  </form>
</section>
{{end}}
<footer>
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/></svg>
  <p>{{.Outcome}} What you enter goes straight from this page to the MCP server on this computer and never passes through the MCP client or the model. Spaces around each value are removed. This page is open until {{.Until.Format "15:04"}}.</p>
</footer>
</main>
</body>
</html>
`
