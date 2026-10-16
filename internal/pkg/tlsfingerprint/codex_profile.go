package tlsfingerprint

// DefaultCodexProfile 返回官方 Codex CLI（Ubuntu x86_64 构建）的 TLS ClientHello
// 指纹参数。数值不是猜测，全部来自 2026-09-13 对官方 codex-cli 0.154.0
// (codex-x86_64-unknown-linux-musl) 在 Ubuntu 24.04 上真实流量的取证抓包
// （链路层 pcap 解析 ClientHello 原始字节）：
//
//   - 传输层：native-tls + OpenSSL 3.x（非 rustls：30 个 cipher suites 含
//     DHE/RSA 旧套件、26 个签名算法含 ed448/rsa_pss_pss 系列、supported_groups
//     含 ffdhe2048/3072、TLS1.2+1.3 双版本、32 字节非零 session_id、无 GREASE）。
//   - cipher suites（30，顺序即线缆顺序）：
//     1302 1303 1301 c02c c030 009f cca9 cca8 ccaa c02b c02f 009e c024 c028
//     006b c023 c027 0067 c00a c014 0039 c009 c013 0033 009d 009c 003d 003c
//     0035 002f
//   - supported_groups（8）：X25519MLKEM768(0x11ec) 在最前，后接 X25519、
//     secp256r1、secp384r1、secp521r1、ffdhe2048(0x0100)、ffdhe3072(0x0101)。
//   - key_share：两组——X25519MLKEM768(keylen 1216) + X25519(keylen 32)。
//   - signature_algorithms（26）：
//     0905 0906 0904 0403 0503 0603 0807 0808 081a 081b 081c 0809 080a 080b
//     0804 0805 0806 0401 0501 0601 0303 0301 0302 0402 0502 0602
//   - supported_versions：[TLS1.3, TLS1.2]；psk_key_exchange_modes: [psk_dhe_ke]。
//   - 扩展顺序（11 个，线缆顺序）：renegotiation_info(65281), server_name(0),
//     ec_point_formats(11), supported_groups(10), session_ticket(35),
//     encrypt_then_mac(22), extended_master_secret(23), signature_algorithms(13),
//     supported_versions(43), psk_key_exchange_modes(45), key_share(51)。
//   - 无 ALPN 扩展：官方客户端不发送 ALPN，与 chatgpt.com 的真实流量因此
//     全部回落 HTTP/1.1。本仓库伪装链路同为 H1（固定头序写出器），行为一致。
//
// 与官方指纹的已知近似：utls 是否能为 X25519MLKEM768 生成本地密钥取决于库
// 版本；若不支持，key_share 退化为仅 X25519（JA3/JA4 均不含 key_share 内容，
// 指纹不受影响）。
func DefaultCodexProfile() *Profile {
	return &Profile{
		Name: "Codex CLI 0.154.0 (Ubuntu x86_64, OpenSSL 3.x)",
		CipherSuites: []uint16{
			0x1302, 0x1303, 0x1301,
			0xc02c, 0xc030, 0x009f, 0xcca9, 0xcca8, 0xccaa,
			0xc02b, 0xc02f, 0x009e, 0xc024, 0xc028, 0x006b,
			0xc023, 0xc027, 0x0067, 0xc00a, 0xc014, 0x0039,
			0xc009, 0xc013, 0x0033, 0x009d, 0x009c, 0x003d,
			0x003c, 0x0035, 0x002f,
		},
		Curves: []uint16{
			0x11ec, // X25519MLKEM768
			0x001d, // X25519
			0x0017, // secp256r1
			0x001e, // X448
			0x0018, // secp384r1
			0x0019, // secp521r1
			0x0100, // ffdhe2048
			0x0101, // ffdhe3072
		},
		PointFormats: []uint16{
			0, // uncompressed
		},
		EnableGREASE: false,
		SignatureAlgorithms: []uint16{
			0x0905, 0x0906, 0x0904, 0x0403, 0x0503, 0x0603,
			0x0807, 0x0808, 0x081a, 0x081b, 0x081c,
			0x0809, 0x080a, 0x080b,
			0x0804, 0x0805, 0x0806,
			0x0401, 0x0501, 0x0601,
			0x0303, 0x0301, 0x0302,
			0x0402, 0x0502, 0x0602,
		},
		// 官方不发 ALPN 扩展：Extensions 列表不含 16 即可（dialer 仅在扩展表
		// 含 16 时写入 ALPN）。ALPNProtocols 留空，不参与构建。
		ALPNProtocols: nil,
		SupportedVersions: []uint16{
			0x0304, // TLS 1.3
			0x0303, // TLS 1.2
		},
		KeyShareGroups: []uint16{
			0x11ec, // X25519MLKEM768（官方线缆顺序在最前）
			0x001d, // X25519
		},
		PSKModes: []uint16{
			1, // psk_dhe_ke
		},
		// 线缆顺序（11 扩展，无 ALPN/无 ECH/无 SCT/无 status_request）。
		Extensions: []uint16{
			0xff01, // renegotiation_info
			0,      // server_name
			11,     // ec_point_formats
			10,     // supported_groups
			35,     // session_ticket
			22,     // encrypt_then_mac
			23,     // extended_master_secret
			13,     // signature_algorithms
			43,     // supported_versions
			45,     // psk_key_exchange_modes
			51,     // key_share
		},
	}
}
