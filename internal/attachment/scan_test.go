package attachment

import "testing"

func TestScan(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nrest-of-image")
	cases := []struct {
		name, mime string
		body       []byte
		want       string
	}{
		{"clean image", "image/png", png, "clean"},
		{"clean log", "text/plain", []byte("INFO started ok"), "clean"},
		{"eicar in image", "image/png", append(append([]byte{}, png...), eicar...), "flagged"},
		{"eicar in log", "text/plain", append([]byte("log "), eicar...), "flagged"},
		{"zip polyglot image", "image/png", append(append([]byte{}, png...), 'P', 'K', 0x03, 0x04), "flagged"},
		{"embedded script in image", "image/png", append(append([]byte{}, png...), []byte("<script>alert(1)</script>")...), "flagged"},
		{"markup in log is fine", "text/plain", []byte("rendered <script> tag in log line"), "clean"},
		{"elf in log", "text/plain", append([]byte("dump: "), 0x7f, 'E', 'L', 'F'), "flagged"},
	}
	for _, c := range cases {
		if got := scan(c.mime, c.body); got != c.want {
			t.Errorf("%s: scan = %q, want %q", c.name, got, c.want)
		}
	}
}
