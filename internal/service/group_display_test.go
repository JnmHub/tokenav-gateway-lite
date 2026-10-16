package service

import "testing"

func TestNormalizeGroupDisplayIdentity(t *testing.T) {
	tests := []struct {
		name      string
		platform  string
		brand     string
		icon      string
		wantBrand string
		wantIcon  string
		wantErr   bool
	}{
		{name: "openai defaults", platform: PlatformOpenAI, wantBrand: "OpenAI", wantIcon: "openai"},
		{name: "kimi defaults", platform: PlatformKimi, wantBrand: "Kimi", wantIcon: "kimi"},
		{name: "zhipu defaults", platform: PlatformZhipu, wantBrand: "智谱 GLM", wantIcon: "glm"},
		{name: "deepseek defaults", platform: PlatformDeepseek, wantBrand: "DeepSeek", wantIcon: "deepseek"},
		{name: "kimi on openai scheduler", platform: PlatformOpenAI, brand: " Kimi ", icon: "KIMI", wantBrand: "Kimi", wantIcon: "kimi"},
		{name: "glm on openai scheduler", platform: PlatformOpenAI, brand: "GLM", icon: "glm", wantBrand: "GLM", wantIcon: "glm"},
		{name: "reject external url", platform: PlatformOpenAI, brand: "Kimi", icon: "https://example.com/icon.svg", wantErr: true},
		{name: "reject long brand", platform: PlatformOpenAI, brand: "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一", icon: "glm", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			brand, icon, err := NormalizeGroupDisplayIdentity(tt.platform, tt.brand, tt.icon)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeGroupDisplayIdentity() error = %v", err)
			}
			if brand != tt.wantBrand || icon != tt.wantIcon {
				t.Fatalf("identity = %q/%q, want %q/%q", brand, icon, tt.wantBrand, tt.wantIcon)
			}
		})
	}
}
