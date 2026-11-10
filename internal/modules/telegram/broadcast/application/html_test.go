package broadcastapp

import "testing"

// TestValidateMessageHTML accepts supported Telegram markup and rejects whole invalid bodies.
func TestValidateMessageHTML(t *testing.T) {
	valid := []string{
		"plain text\nnext line", "&lt;hello&gt; &amp; &quot;world&quot; &#39; &#x1F600;",
		"<b>bold <i>italic</i></b><strong>strong</strong><em>em</em>",
		"<u>u</u><ins>ins</ins><s>s</s><strike>s</strike><del>del</del>",
		`<a href="https://example.com?a=1&amp;b=2">link</a><a href="tg://user?id=123">mention</a>`,
		`<span class="tg-spoiler">hidden</span><tg-spoiler>hidden</tg-spoiler>`,
		`<pre><code class="language-go">fmt.Println(&quot;hi&quot;)</code></pre>`,
		`<blockquote>quote</blockquote><blockquote expandable>expanded</blockquote>`,
		`<tg-emoji emoji-id="5368324170671202286">emoji</tg-emoji>`,
	}
	for _, body := range valid {
		if err := ValidateMessageHTML(body); err != nil {
			t.Errorf("valid HTML rejected: %q: %v", body, err)
		}
	}
	invalid := []string{"</root><root>x", "<b><code>x</code></b>", "", "<p>x</p>", "<div>x</div>", "1 < 2", "x & y", "&nbsp;", "&apos;", "<b>open", "<b><i>x</b></i>", "<!--x-->text", "<!DOCTYPE x>text", `<a href="javascript:alert(1)">bad</a>`, `<b onclick="bad">x</b>`, "<code><b>x</b></code>", "<blockquote><blockquote>x</blockquote></blockquote>", `<tg-emoji emoji-id="no">x</tg-emoji>`, `<span class="other">x</span>`}
	for _, body := range invalid {
		if err := ValidateMessageHTML(body); err == nil {
			t.Errorf("invalid HTML accepted: %q", body)
		}
	}
}
