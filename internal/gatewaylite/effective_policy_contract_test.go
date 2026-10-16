package gatewaylite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const effectivePolicyV1ContractChecksum = "5b34047db98c0bd083a5f94a2cc85237a348d0218f1f639f4478092a82a566b4"

func TestEffectivePolicyV1ContractChecksum(t *testing.T) {
	// gateway-lite 需要能够作为独立源码包运行测试，因此保留本项目自己的契约副本。
	// 固定 checksum 门禁仍会阻止它与主站共享契约静默漂移。
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "effective-policy-v1.json"))
	if err != nil {
		t.Fatalf("read effective policy contract: %v", err)
	}
	var policy GatewayEffectivePolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode effective policy contract: %v", err)
	}
	checksum, err := EffectivePolicyChecksum(policy)
	if err != nil {
		t.Fatalf("checksum effective policy contract: %v", err)
	}
	if checksum != effectivePolicyV1ContractChecksum {
		t.Fatalf("effective policy v1 checksum=%s, want %s", checksum, effectivePolicyV1ContractChecksum)
	}
}
