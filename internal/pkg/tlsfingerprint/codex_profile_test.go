package tlsfingerprint

import (
	"testing"
)

// TestDefaultCodexProfile 参数快照：数值来自 2026-09-13 官方 codex-cli 0.154.0
// Ubuntu x86_64 真实流量取证（tmp/capcap 抓包，ClientHello 字节级解析）。
// 任何变更都意味着偏离官方指纹，必须是有意的决策。
func TestDefaultCodexProfile(t *testing.T) {
	p := DefaultCodexProfile()

	// 30 个 cipher suites，线缆顺序（OpenSSL 3.x 特征：含 DHE/RSA 旧套件）。
	wantCiphers := []uint16{
		0x1302, 0x1303, 0x1301,
		0xc02c, 0xc030, 0x009f, 0xcca9, 0xcca8, 0xccaa,
		0xc02b, 0xc02f, 0x009e, 0xc024, 0xc028, 0x006b,
		0xc023, 0xc027, 0x0067, 0xc00a, 0xc014, 0x0039,
		0xc009, 0xc013, 0x0033, 0x009d, 0x009c, 0x003d,
		0x003c, 0x0035, 0x002f,
	}
	if len(p.CipherSuites) != len(wantCiphers) {
		t.Fatalf("cipher suites len = %d, want %d", len(p.CipherSuites), len(wantCiphers))
	}
	for i, c := range wantCiphers {
		if p.CipherSuites[i] != c {
			t.Fatalf("cipher suites[%d] = %04x, want %04x", i, p.CipherSuites[i], c)
		}
	}

	// TLS1.3 + TLS1.2 双版本（官方未启用 rustls；这是 OpenSSL 行为）。
	wantVersions := []uint16{0x0304, 0x0303}
	if len(p.SupportedVersions) != 2 {
		t.Fatalf("supported versions = %x, want %x", p.SupportedVersions, wantVersions)
	}

	// 8 个 groups：X25519MLKEM768 在最前，含 X448 与 ffdhe（OpenSSL 特征）。
	wantCurves := []uint16{0x11ec, 0x001d, 0x0017, 0x001e, 0x0018, 0x0019, 0x0100, 0x0101}
	if len(p.Curves) != len(wantCurves) {
		t.Fatalf("curves = %x, want %x", p.Curves, wantCurves)
	}
	for i, c := range wantCurves {
		if p.Curves[i] != c {
			t.Fatalf("curves[%d] = %04x, want %04x", i, p.Curves[i], c)
		}
	}

	// key_share 两组：X25519MLKEM768 + X25519。
	if len(p.KeyShareGroups) != 2 || p.KeyShareGroups[0] != 0x11ec || p.KeyShareGroups[1] != 0x001d {
		t.Fatalf("key share groups = %x, want [11ec 001d]", p.KeyShareGroups)
	}

	// 26 个签名算法（OpenSSL 3.x 特征：含 ed448/rsa_pss_pss/sha224 系列）。
	if len(p.SignatureAlgorithms) != 26 {
		t.Fatalf("sig algs len = %d, want 26", len(p.SignatureAlgorithms))
	}
	if p.SignatureAlgorithms[0] != 0x0905 || p.SignatureAlgorithms[25] != 0x0602 {
		t.Fatalf("sig algs 首尾不符: %04x ... %04x", p.SignatureAlgorithms[0], p.SignatureAlgorithms[25])
	}

	// 无 GREASE、无 ALPN（官方不发 ALPN，流量回落 HTTP/1.1）。
	if p.EnableGREASE {
		t.Fatal("官方 codex 指纹不含 GREASE")
	}
	if len(p.ALPNProtocols) != 0 {
		t.Fatalf("alpn = %v, want 空（不发 ALPN 扩展）", p.ALPNProtocols)
	}
	for _, ext := range p.Extensions {
		switch ext {
		case 16, 18, 5, 0xfe0d:
			t.Fatalf("扩展 %d 不属于官方 codex 指纹集合（无 ALPN/SCT/OCSP/ECH）", ext)
		}
	}

	// 扩展序 11 个，renegotiation_info 打头、key_share 收尾（取证线缆顺序）。
	if len(p.Extensions) != 11 || p.Extensions[0] != 0xff01 || p.Extensions[10] != 51 {
		t.Fatalf("extensions = %x, want [ff01 ... 51] 共 11 个", p.Extensions)
	}
}
