package anikoto

import "testing"

// TestParseServerListAttribution proves server rows follow their enclosing
// audio block even with nested markup inside a block, and that rows
// outside any block are ignored.
func TestParseServerListAttribution(t *testing.T) {
	htmlStr := `<div class="tip">tip</div>` +
		`<div class="servers">` +
		`<div class="type" data-type="sub"><label><i></i> SUB</label><ul>` +
		`<li data-ep-id="e1" data-sv-id="1" data-link-id="sublink1">Vidstream-2</li>` +
		`<li data-ep-id="e1" data-sv-id="2" data-link-id="sublink2">HD-1</li>` +
		`</ul><div class="nested"><span>note</span></div></div>` +
		`<div class="type" data-type="dub"><label>DUB</label><ul>` +
		`<li data-ep-id="e1" data-sv-id="3" data-link-id="dublink1">Vidstream-2</li>` +
		`</ul></div></div>` +
		`<li data-sv-id="x" data-link-id="stray">Stray</li>`

	sub := parseServerList(htmlStr, "sub")
	if len(sub) != 2 || sub[0].linkID != "sublink1" || sub[1].linkID != "sublink2" {
		t.Errorf("sub servers = %+v, want both sub links", sub)
	}
	dub := parseServerList(htmlStr, "dub")
	if len(dub) != 1 || dub[0].linkID != "dublink1" {
		t.Errorf("dub servers = %+v, want the dub link only", dub)
	}
	if got := parseServerList(htmlStr, "raw"); len(got) != 0 {
		t.Errorf("unknown category = %+v, want empty", got)
	}
	if got := parseServerList(`<div class="servers"></div>`, "sub"); len(got) != 0 {
		t.Errorf("empty list = %+v, want empty", got)
	}
}
