package markdown

import (
	"strings"
	"testing"
)

func TestSurfacesRenderAsAChoice(t *testing.T) {
	source := "Intro.\n\n```surfaces\nWeb: **Server** → [Security](/docs/guides/security)\nCLI: moin integrations cloudflare\nnot a surface line\nDesktop: ignored\nMCP: set_integrations\nAPI: PUT /api/v1/integrations\n```\n"
	html, err := NewParser().Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	out := string(html)

	for _, want := range []string{
		`data-surfaces`,
		`role="radiogroup"`,
		`type="radio" id="surface-1-web" name="surface-1" value="web" checked`,
		`<strong>Server</strong>`,
		`<a href="/docs/guides/security">Security</a>`,
		`<pre class="m-0 rounded-none bg-transparent px-6 py-4 pr-14"><code>moin integrations cloudflare</code></pre>`,
		`<code>PUT /api/v1/integrations</code>`,
		`<code>set_integrations</code>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"not a surface line", "Desktop", "ignored", "language-surfaces", ">TUI<"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in\n%s", unwanted, out)
		}
	}

	// The choice follows the surface order, not the order of the lines,
	// and the first is picked.
	order := []string{">Web<", ">CLI<", ">API<", ">MCP<"}
	last := -1
	for _, label := range order {
		i := strings.Index(out, label)
		if i < last {
			t.Errorf("%s out of order", label)
		}
		last = i
	}
	if strings.Count(out, ` checked`) != 1 || strings.Count(out, `data-surface="`) != 4 {
		t.Errorf("want the first surface picked and four parts:\n%s", out)
	}
}

func TestOtherCodeBlocksStay(t *testing.T) {
	html, err := NewParser().Parse([]byte("```bash\nmoin deploy\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `<pre><code class="language-bash">moin deploy`) {
		t.Errorf("got %s", html)
	}
}

func TestPlainSurfaces(t *testing.T) {
	source := "# Keys\n\n```surfaces\nWeb: **Keys**\nCLI: moin integrations\nCLI: moin integrations rm `x`\nnoise\n```\n\n```bash\nmoin deploy\n```\n"
	want := "# Keys\n\n- Web: **Keys**\n- CLI: `moin integrations`\n- CLI: `` moin integrations rm `x` ``\n\n```bash\nmoin deploy\n```\n"
	if got := string(PlainSurfaces([]byte(source))); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestSurfacesLongCommand(t *testing.T) {
	source := "```surfaces\nCLI: moin buckets add \\\n  --bucket b \\\n  --access-key k\nMCP: add_bucket\n```\n"
	html, err := NewParser().Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "<code>moin buckets add \\\n  --bucket b \\\n  --access-key k</code>") {
		t.Errorf("got %s", html)
	}
	want := "- CLI:\n\n  ```\n  moin buckets add \\\n    --bucket b \\\n    --access-key k\n  ```\n- MCP: `add_bucket`\n"
	if got := string(PlainSurfaces([]byte(source))); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
