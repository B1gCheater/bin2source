package emit

import "strings"

// Sanitize turns an arbitrary file name into an identifier base in
// lower_snake form.
//
// Every run of characters outside [A-Za-z0-9] collapses into a single
// underscore, which is dropped at the start and the end. A leading digit gets
// an underscore prefix so the result is legal in all five target languages.
//
// Sanitize returns an empty string when nothing usable is left, for example
// for a fully non-ASCII name. Callers must reject that case and ask the user
// for -name instead of guessing.
func Sanitize(s string) string {
	var b strings.Builder
	pending := false
	for _, r := range s {
		if !isASCIIAlnum(r) {
			pending = true
			continue
		}
		if pending && b.Len() > 0 {
			b.WriteByte('_')
		}
		pending = false
		b.WriteRune(r)
	}

	out := strings.ToLower(b.String())
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return out
}

// LowerSnake is the C and C++ convention: logo_png.
func LowerSnake(base string) string { return base }

// UpperSnake is the Rust and Python convention: LOGO_PNG.
func UpperSnake(base string) string { return strings.ToUpper(base) }

// Pascal is the Go convention: LogoPng. The result is exported so other
// packages can reach it.
//
// A leading underscore, which Sanitize adds when the name starts with a
// digit, is preserved because Go identifiers cannot start with a digit
// either.
func Pascal(base string) string {
	prefix := ""
	if strings.HasPrefix(base, "_") {
		prefix = "_"
		base = base[1:]
	}

	var b strings.Builder
	for _, part := range strings.Split(base, "_") {
		if part == "" {
			continue
		}
		b.WriteString(capitalizeFirstLetter(part))
	}
	return prefix + b.String()
}

// joinedIdent builds an identifier for one of the constants the generators
// write alongside the array, by joining the array's name to a suffix.
//
// cas is the language's casing convention applied to each part separately, so
// Go's Pascal turns "logo_png" plus "zstd_sha256" into LogoPngZstdSha256 while
// C's identity gives logo_png_zstd_sha256.
//
// An empty suffix means the array's own identifier, which is the base name
// alone and carries no separator.
func joinedIdent(base, sep, suffix string, cas func(string) string) string {
	if suffix == "" {
		return cas(base)
	}
	return cas(base) + sep + cas(suffix)
}

// capitalizeFirstLetter upper cases the first ASCII letter of s. Going by
// letter rather than by first byte matters when a word starts with a digit:
// "3d_model" should read as 3DModel, not 3dModel.
func capitalizeFirstLetter(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return s[:i] + string(s[i]-'a'+'A') + s[i+1:]
		}
		if s[i] >= 'A' && s[i] <= 'Z' {
			return s // already capitalised
		}
	}
	return s // no letters at all
}

func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}
