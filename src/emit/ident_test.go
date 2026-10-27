package emit

import "testing"

func TestSanitize(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// The rule the tool documents: logo.png becomes the base logo_png.
		{"logo.png", "logo_png"},
		{"logo.PNG", "logo_png"},
		{"asset.tar.gz", "asset_tar_gz"},
		{"my-logo v2.png", "my_logo_v2_png"},

		// A leading digit needs an underscore in every target language.
		{"1logo.png", "_1logo_png"},
		{"9", "_9"},

		// Runs of separators collapse, and the ends are trimmed.
		{"a---b", "a_b"},
		{"-leading", "leading"},
		{"trailing-", "trailing"},
		{"__already__", "already"},
		{"", ""},

		// Non-ASCII cannot be represented safely, so it acts as a separator.
		{"图标.png", "png"},
		{"...", ""},
	}

	for _, tc := range tests {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCaseConventions(t *testing.T) {
	tests := []struct {
		base  string
		snake string
		upper string
		go_   string
	}{
		{"logo_png", "logo_png", "LOGO_PNG", "LogoPng"},
		{"a", "a", "A", "A"},
		{"asset_tar_gz", "asset_tar_gz", "ASSET_TAR_GZ", "AssetTarGz"},
		// The underscore that guards a leading digit must survive Pascal,
		// and the first letter after the digits is the one capitalised.
		{"_1logo_png", "_1logo_png", "_1LOGO_PNG", "_1LogoPng"},
		{"_3d_model", "_3d_model", "_3D_MODEL", "_3DModel"},
	}

	for _, tc := range tests {
		if got := LowerSnake(tc.base); got != tc.snake {
			t.Errorf("LowerSnake(%q) = %q, want %q", tc.base, got, tc.snake)
		}
		if got := UpperSnake(tc.base); got != tc.upper {
			t.Errorf("UpperSnake(%q) = %q, want %q", tc.base, got, tc.upper)
		}
		if got := Pascal(tc.base); got != tc.go_ {
			t.Errorf("Pascal(%q) = %q, want %q", tc.base, got, tc.go_)
		}
	}
}
