package textclean

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripPrefersPlain(t *testing.T) {
	tests := []struct {
		name  string
		plain string
		html  string
		want  string
	}{
		{
			name:  "plain wins",
			plain: "Your parcel is on its way.",
			html:  "<p>Something else entirely</p>",
			want:  "Your parcel is on its way.",
		},
		{
			name:  "empty plain falls back to html",
			plain: "",
			html:  "<p>Your parcel is on its way.</p>",
			want:  "Your parcel is on its way.",
		},
		{
			name:  "blank plain falls back to html",
			plain: "   \n\t  ",
			html:  "<p>Your parcel is on its way.</p>",
			want:  "Your parcel is on its way.",
		},
		{
			name:  "neither",
			plain: "",
			html:  "",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip(tt.plain, tt.html, 0); got != tt.want {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripHTML(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			name: "script content is removed",
			html: `<div>Hello<script type="text/javascript">alert("buy now")</script>there</div>`,
			want: "Hello\nthere",
		},
		{
			name: "style content is removed",
			html: "<style>.a { color: red }</style><p>Hello</p>",
			want: "Hello",
		},
		{
			name: "comments are removed",
			html: "<p>Hello<!-- hidden tracking --> there</p>",
			want: "Hello there",
		},
		{
			name: "br becomes a newline",
			html: "one<br>two<br/>three<BR />four",
			want: "one\ntwo\nthree\nfour",
		},
		{
			name: "block tags become newlines",
			html: "<ul><li>one</li><li>two</li></ul><table><tr><td>cell</td></tr></table>",
			want: "one\ntwo\ncell",
		},
		{
			name: "other tags are dropped but their text kept",
			html: `<p>Your <b>order</b> from <a href="https://example.com/x?a=1&amp;b=2">Amazon</a> shipped</p>`,
			want: "Your order from Amazon shipped",
		},
		{
			name: "entities are unescaped",
			html: "<p>Tom &amp; Jerry &lt;3 &quot;quotes&quot; &nbsp;&#39;s</p>",
			want: `Tom & Jerry <3 "quotes" 's`,
		},
		{
			name: "escaped markup is not treated as a tag",
			html: "<p>Write &lt;script&gt;alert(1)&lt;/script&gt; safely</p>",
			want: "Write <script>alert(1)</script> safely",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip("", tt.html, 0); got != tt.want {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripCollapsesWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		plain string
		want  string
	}{
		{name: "runs of spaces", plain: "one    two\t\tthree", want: "one two three"},
		{name: "blank lines", plain: "one\n\n\n\ntwo", want: "one\ntwo"},
		{name: "trailing and leading", plain: "\n\n  hello  \n\n", want: "hello"},
		{name: "windows line endings", plain: "one\r\n\r\ntwo\rthree", want: "one\ntwo\nthree"},
		{name: "line-internal padding", plain: "one   \n   two", want: "one\ntwo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip(tt.plain, "", 0); got != tt.want {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripRemovesInvisibleCharacters(t *testing.T) {
	tests := []struct {
		name  string
		plain string
		want  string
	}{
		{name: "zero width space", plain: "in\u200bvoice", want: "invoice"},
		{name: "zero width non-joiner and joiner", plain: "in\u200cvo\u200dice", want: "invoice"},
		{name: "bidi marks", plain: "in\u200evo\u200fice", want: "invoice"},
		{name: "bidi overrides", plain: "\u202ainvoice\u202c", want: "invoice"},
		{name: "word joiner", plain: "in\u2060voice", want: "invoice"},
		{name: "invisible plus", plain: "in\u2064voice", want: "invoice"},
		{name: "byte order mark", plain: "\ufeffinvoice", want: "invoice"},
		{name: "visible text is untouched", plain: "Café — naïve", want: "Café — naïve"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Strip(tt.plain, "", 0); got != tt.want {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripDropsQuotedRepliesAndSignatures(t *testing.T) {
	tests := []struct {
		name  string
		plain string
		want  string
	}{
		{
			name:  "on ... wrote",
			plain: "Sure, Thursday works.\nOn Tue, 5 Mar 2024 at 10:00, Alice <alice@example.com> wrote:\nAre you free?",
			want:  "Sure, Thursday works.",
		},
		{
			name:  "on ... wrote is case-insensitive",
			plain: "Yes.\nON TUE, 5 MAR 2024, BOB WROTE:\nsomething",
			want:  "Yes.",
		},
		{
			name:  "quote markers",
			plain: "Agreed.\n> previous message\n> more of it",
			want:  "Agreed.",
		},
		{
			name:  "indented quote markers",
			plain: "Agreed.\n   > previous message",
			want:  "Agreed.",
		},
		{
			name:  "signature separator",
			plain: "See you then.\n-- \nAlice\nHead of Things\n07700 900000",
			want:  "See you then.",
		},
		{
			name:  "whichever comes first",
			plain: "Body.\n-- \nAlice\nOn Tue, Bob wrote:\nold",
			want:  "Body.",
		},
		{
			name:  "a line merely mentioning wrote is kept",
			plain: "I wrote: the report is done\nSecond line",
			want:  "I wrote: the report is done\nSecond line",
		},
		{
			name:  "a dash line that is not a signature is kept",
			plain: "Totals\n---\nTen",
			want:  "Totals\n---\nTen",
		},
		{
			name:  "html quoted reply",
			plain: "",
			want:  "Sure, Thursday works.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := ""
			if tt.plain == "" {
				html = "<div>Sure, Thursday works.</div><div>On Tue, 5 Mar 2024, Alice wrote:</div><blockquote>Are you free?</blockquote>"
			}
			if got := Strip(tt.plain, html, 0); got != tt.want {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripCaps(t *testing.T) {
	tests := []struct {
		name  string
		plain string
		max   int
		want  string
	}{
		{name: "under the cap", plain: "short", max: 10, want: "short"},
		{name: "exactly the cap", plain: "0123456789", max: 10, want: "0123456789"},
		{name: "over the cap", plain: "0123456789abc", max: 10, want: "0123456789" + Truncated},
		{name: "trailing space before the marker is trimmed", plain: "one two three", max: 8, want: "one two" + Truncated},
		{name: "no cap", plain: "0123456789abc", max: 0, want: "0123456789abc"},
		{name: "negative cap", plain: "0123456789abc", max: -1, want: "0123456789abc"},
		{name: "counts runes not bytes", plain: "ααααα", max: 3, want: "ααα" + Truncated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Strip(tt.plain, "", tt.max)
			if got != tt.want {
				t.Errorf("Strip = %q, want %q", got, tt.want)
			}
			if tt.max > 0 && !strings.HasSuffix(got, Truncated) && len([]rune(got)) > tt.max {
				t.Errorf("uncut result %q is longer than max %d", got, tt.max)
			}
		})
	}
}

// TestStripNewsletter runs a realistic HTML newsletter end to end.
func TestStripNewsletter(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "newsletter.html"))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	got := Strip("", string(raw), 120)

	for _, unwanted := range []string{"<", ">", "trackPixel", "font-family", "\u200b"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("stripped text still contains %q: %q", unwanted, got)
		}
	}
	if !strings.HasPrefix(got, "Your order has been dispatched") {
		t.Errorf("unexpected start: %q", got)
	}
	if !strings.HasSuffix(got, Truncated) {
		t.Errorf("want the body capped and marked, got %q", got)
	}
	if n := len([]rune(got)) - len([]rune(Truncated)); n > 120 {
		t.Errorf("body is %d runes, want at most 120", n)
	}
}
