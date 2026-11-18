package feedback

import (
	"encoding/json"
	"strings"
	"testing"
)

// The inbox renders the submitter and the origin of a report straight off the
// list row, so the projection has to carry both — but only those two context
// fields: a page is 50 rows, and one project's contexts are bounded only by
// the ingest body limit.
func TestListProjectionCarriesSubmitterAndSource(t *testing.T) {
	email := "chen.wei@example.com"
	it := Item{
		FeedbackID:   "fb-1",
		ContactEmail: &email,
		Context: `{"url":"https://example.com/pricing","user_agent":"Chrome/128",` +
			`"custom":"` + strings.Repeat("x", 900) + `"}`,
		Type:     "bug",
		Message:  "上传失败",
		Status:   "open",
		Priority: "urgent",
	}

	got := it.list()

	if got.ContactEmail == nil || *got.ContactEmail != email {
		t.Fatalf("contact email = %v, want %q", got.ContactEmail, email)
	}
	if got.URL == nil || *got.URL != "https://example.com/pricing" {
		t.Fatalf("url = %v, want the submitted url", got.URL)
	}
	if got.UserAgent == nil || *got.UserAgent != "Chrome/128" {
		t.Fatalf("user agent = %v, want the submitted user agent", got.UserAgent)
	}
	if got.Tags == nil {
		t.Fatal("tags = nil, want an empty slice so the client never sees null")
	}
}

func TestListProjectionOmitsAnAbsentOrUnreadableSource(t *testing.T) {
	for name, item := range map[string]Item{
		"no context":      {FeedbackID: "fb-2"},
		"empty object":    {FeedbackID: "fb-3", Context: "{}"},
		"invalid json":    {FeedbackID: "fb-4", Context: "{oops"},
		"unrelated  keys": {FeedbackID: "fb-5", Context: `{"release":"1.4.0"}`},
	} {
		t.Run(name, func(t *testing.T) {
			got := item.list()
			if got.URL != nil || got.UserAgent != nil {
				t.Fatalf("url/user agent = %v/%v, want both nil", got.URL, got.UserAgent)
			}
			if got.ContactEmail != nil {
				t.Fatalf("contact email = %v, want nil for an anonymous submission", *got.ContactEmail)
			}
		})
	}
}

// Depth and total size are the two caps docs/feedback-system-design.md §6.6
// asks for that the original sanitiser did not enforce.
func TestSanitizeContextCaps(t *testing.T) {
	t.Run("drops blacklisted keys at every level", func(t *testing.T) {
		got := sanitizeContext(map[string]any{
			"release": "1.4.0",
			"Token":   "live-token",
			"nested":  map[string]any{"cookie": "sid=1", "screen": "Home"},
		})
		if _, ok := got["Token"]; ok {
			t.Fatal("top-level blacklisted key survived")
		}
		nested := got["nested"].(map[string]any)
		if _, ok := nested["cookie"]; ok {
			t.Fatal("nested blacklisted key survived")
		}
		if nested["screen"] != "Home" {
			t.Fatalf("nested payload = %v, want the non-blacklisted value kept", nested)
		}
	})

	t.Run("truncates long values and caps the key count", func(t *testing.T) {
		in := map[string]any{"long": strings.Repeat("x", contextMaxValue+500)}
		for i := range contextMaxKeys + 20 {
			in[string(rune('a'+i%26))+string(rune('0'+i/26))] = i
		}
		got := sanitizeContext(in)
		if len(got) > contextMaxKeys {
			t.Fatalf("kept %d keys, want at most %d", len(got), contextMaxKeys)
		}
		if long, ok := got["long"].(string); ok && len(long) != contextMaxValue {
			t.Fatalf("long value kept %d bytes, want %d", len(long), contextMaxValue)
		}
	})

	t.Run("stops recursing past the depth cap", func(t *testing.T) {
		deep := map[string]any{"leaf": "kept"}
		for range contextMaxDepth + 3 {
			deep = map[string]any{"child": deep}
		}
		got := sanitizeContext(deep)

		level := got
		for depth := 0; ; depth++ {
			child, ok := level["child"].(map[string]any)
			if !ok {
				if depth >= contextMaxDepth {
					t.Fatalf("recursed %d levels, want at most %d", depth, contextMaxDepth)
				}
				return
			}
			level = child
		}
	})

	t.Run("applies the caps inside arrays too", func(t *testing.T) {
		// A breadcrumb or event list is an ordinary custom-context shape, and
		// without the []any arm one array level routes around every cap.
		got := sanitizeContext(map[string]any{
			"events": []any{
				map[string]any{"cookie": "sid=1", "name": "click"},
				strings.Repeat("z", contextMaxValue+500),
			},
		})

		events := got["events"].([]any)
		first := events[0].(map[string]any)
		if _, ok := first["cookie"]; ok {
			t.Fatal("blacklisted key inside an array survived")
		}
		if first["name"] != "click" {
			t.Fatalf("array element = %v, want the non-blacklisted value kept", first)
		}
		if long := events[1].(string); len(long) != contextMaxValue {
			t.Fatalf("array string kept %d bytes, want %d", len(long), contextMaxValue)
		}
	})

	t.Run("strips query and fragment from url and referrer", func(t *testing.T) {
		// Ingest is the trust boundary: the write key is public, so an old SDK
		// build or a direct POST can carry a reset token in the address.
		got := sanitizeContext(map[string]any{
			"url":      "https://app.example.com/reset?token=live#access_token=xyz",
			"referrer": "https://sso.example.com/callback?code=secret",
			"note":     "https://example.com/keep?this=1",
		})

		if got["url"] != "https://app.example.com/reset" {
			t.Fatalf("url = %v, want the query and fragment gone", got["url"])
		}
		if got["referrer"] != "https://sso.example.com/callback" {
			t.Fatalf("referrer = %v, want the query gone", got["referrer"])
		}
		if got := sanitizeContext(map[string]any{
			"url": "https://user:secret@tools.example.com/report?x=1",
		}); got["url"] != "https://tools.example.com/report" {
			t.Fatalf("url = %v, want the userinfo gone too", got["url"])
		}
		// Only the two address keys are rewritten; other values are the
		// customer's own and are left alone.
		if got["note"] != "https://example.com/keep?this=1" {
			t.Fatalf("note = %v, want it untouched", got["note"])
		}
	})

	t.Run("strips addresses whatever the key's case or shape", func(t *testing.T) {
		// encoding/json matches tags case-insensitively, so "URL" reaches
		// ListItem.URL exactly like "url" — and must be stripped exactly like
		// it. An array under the same key is another way past a top-level,
		// string-only gate.
		got := sanitizeContext(map[string]any{
			"URL":      "https://app.example.com/reset?token=live",
			"Referrer": "https://sso.example.com/callback#code=secret",
			"url_list": []any{"https://app.example.com/a?token=live"},
			"url":      []any{"https://app.example.com/b?token=live"},
		})

		if got["URL"] != "https://app.example.com/reset" {
			t.Fatalf("URL = %v, want the query gone", got["URL"])
		}
		if got["Referrer"] != "https://sso.example.com/callback" {
			t.Fatalf("Referrer = %v, want the fragment gone", got["Referrer"])
		}
		if inList := got["url"].([]any); inList[0] != "https://app.example.com/b" {
			t.Fatalf("url[0] = %v, want the query gone inside the array too", inList[0])
		}
		// A key that merely contains "url" is not an address key.
		if other := got["url_list"].([]any); other[0] != "https://app.example.com/a?token=live" {
			t.Fatalf("url_list[0] = %v, want a non-address key untouched", other[0])
		}
	})

	t.Run("one non-string address does not blank the whole source subline", func(t *testing.T) {
		got := (&Item{Context: `{"url":["https://a/b"],"user_agent":"Chrome/128"}`}).list()

		if got.URL != nil {
			t.Fatalf("url = %v, want nil for a non-string value", *got.URL)
		}
		if got.UserAgent == nil || *got.UserAgent != "Chrome/128" {
			t.Fatalf("user agent = %v, want it to survive its sibling's shape", got.UserAgent)
		}
	})

	t.Run("the size-cap rescue cannot itself exceed the cap", func(t *testing.T) {
		// The four rescue keys are caller-chosen, so parking the payload under
		// one of them must not put it back over the ceiling that just
		// rejected it.
		bulk := []any{}
		for range contextMaxKeys {
			bulk = append(bulk, strings.Repeat("z", contextMaxValue))
		}
		in := map[string]any{"url": bulk, "user_agent": strings.Repeat("u", contextMaxValue+500)}
		for i := range 30 {
			in["k"+string(rune('a'+i))] = strings.Repeat("y", contextMaxValue)
		}

		got := sanitizeContext(in)

		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if len(encoded) > contextMaxBytes {
			t.Fatalf("rescued context is %d bytes, want at most %d", len(encoded), contextMaxBytes)
		}
		if _, ok := got["url"]; ok {
			t.Fatal("a non-string rescue key survived; only short scalars may")
		}
		if ua := got["user_agent"].(string); len(ua) != contextMaxValue {
			t.Fatalf("rescued user_agent is %d bytes, want it truncated to %d", len(ua), contextMaxValue)
		}
	})

	t.Run("keeps the source fields when the context is over the total size cap", func(t *testing.T) {
		in := map[string]any{
			"url":        "https://example.com/pricing",
			"user_agent": "Chrome/128",
			"language":   "zh-CN",
			"referrer":   "https://google.com/",
		}
		// Each value sits under the per-value cap, so only the total trips it.
		for i := range 30 {
			in["k"+string(rune('a'+i))] = strings.Repeat("y", contextMaxValue)
		}

		got := sanitizeContext(in)

		if got["url"] != "https://example.com/pricing" || got["user_agent"] != "Chrome/128" {
			t.Fatalf("got %v, want the row's source fields to survive", got)
		}
		if got["language"] != "zh-CN" || got["referrer"] != "https://google.com/" {
			t.Fatalf("got %v, want every SDK-sent source field to survive", got)
		}
		if len(got) != 4 {
			t.Fatalf("kept %d keys, want only the source fields", len(got))
		}
	})
}
