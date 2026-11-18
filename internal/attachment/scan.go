package attachment

import "bytes"

// eicar is the industry-standard antivirus test string, assembled so this
// source file is not itself flagged by scanners.
var eicar = []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$` + `EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)

// Signatures that have no business inside an allowed upload (images or
// plain-text logs). Leading executable magic is already rejected by the MIME
// sniff; these catch polyglots — payloads embedded after a valid header.
// ponytail: signature-based inline scan; swap for a real AV engine (ClamAV)
// when Phase 2 adds the async scan queue.
var badSignatures = [][]byte{
	eicar,
	{0x7f, 'E', 'L', 'F'},    // ELF executable
	{'M', 'Z', 0x90, 0x00},   // PE/DOS executable (common stub)
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O fat binary
	[]byte("<?php"),          // embedded PHP
	[]byte("<script"),        // embedded HTML script
	{'P', 'K', 0x03, 0x04},   // ZIP appended to an image (polyglot)
}

// scan returns "clean" or "flagged". Flagged objects are stored but
// quarantined: ServeDownload refuses anything not marked clean.
func scan(mime string, body []byte) string {
	sigs := badSignatures
	if mime == "text/plain" {
		// Logs legitimately contain markup; only match the AV test
		// string and raw executable magic there.
		sigs = [][]byte{eicar, badSignatures[1], badSignatures[2], badSignatures[3]}
	}
	for _, sig := range sigs {
		if bytes.Contains(body, sig) {
			return "flagged"
		}
	}
	return "clean"
}
