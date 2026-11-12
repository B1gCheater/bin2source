# bin2source

**Turn any file into source code.** Embed images, configuration files, certificates, or other binary assets directly into C, C++, Go, Rust, Python, or Java projects.

`bin2source` converts a file into a language-native byte array or encoded string. The generated source can be compiled or imported with your application, so you can distribute an asset as source instead of as a separate resource file.

## ✨ Features

- Generate a single source file for **C**, **C++**, **Go**, **Rust**, **Python**, or **Java**.
- Optionally compress with raw **DEFLATE** or **Zstandard**.
- Optionally encrypt with **AES-256-CTR** or **AES-256-CBC** with PKCS#7 padding.
- Choose whether the encryption IV is stored in the array or only in its source comment.
- Use **Base64** output to reduce generated source size.
- Include lengths and SHA-256 constants for inspecting and validating the generated data.
- Build native or cross-platform binaries with Go and `CGO_ENABLED=0`.

## 🚀 Quick start

Generate a C header from an image:

```sh
bin2source -f assets/logo.png -c
```

This creates `logo_png.h` in the current directory. Choose a different target language with exactly one of `-c`, `-cpp`, `-go`, `-rust`, `-python`, or `-java`:

```sh
bin2source -f assets/logo.png -cpp      # logo_png.hpp
bin2source -f assets/logo.png -go       # logo_png.go
bin2source -f assets/logo.png -rust     # logo_png.rs
bin2source -f assets/logo.png -python   # logo_png.py
bin2source -f assets/logo.png -java     # LogoPng.java
```

To put the generated file in a directory, pass that directory to `-o`:

```sh
bin2source -f assets/logo.png -rust -o generated/
```

The output directory is created when needed. Use `-name` to choose the array identifier and default output name, or `-o` to choose a specific output file:

```sh
bin2source -f assets/logo.png -go -name app_logo -pkg assets -o generated/
bin2source -f assets/logo.png -python -o generated/custom_logo.py
```

Run `bin2source -h` for the complete command-line help.

## 🛠️ Build

You need Go **1.25.6 or later**. The project has no cgo requirement.

On Linux and macOS:

```sh
./build.sh
./build.sh linux amd64
./build.sh linux arm64
```

On Windows with PowerShell 7 or later:

```powershell
./build.ps1
./build.ps1 linux amd64
./build.ps1 linux arm64
```

The native build is written to `bin/`. Cross-compiled binaries include the target OS and architecture in their names, such as `bin/bin2source_linux_amd64`.

## ⚙️ Options

### Input and output

| Option | Description |
| --- | --- |
| `-f`, `--file <path>` | Input file. Required. |
| `-c` | Generate a C header (`.h`). |
| `-cpp` | Generate a C++ header (`.hpp`). |
| `-go` | Generate a Go source file (`.go`). |
| `-rust` | Generate a Rust source file (`.rs`). |
| `-python` | Generate a Python module (`.py`). |
| `-java` | Generate a Java source file (`.java`). |
| `-o`, `--output <path>` | Output file or directory. If omitted, the output is written to the current directory. |
| `-name <identifier>` | Override the identifier derived from the file name. |
| `-pkg <identifier>` | Set the Go package name, Java package statement, or wrap Rust output in a module. Go defaults to `main`; Java and Rust have no package/module wrapper by default. |

Specify exactly one target language. By default, the input file name is converted into an ASCII identifier and the language extension is appended; for example, `logo.png` becomes `logo_png.h` for C. Java is the exception: a public class has to live in a file of the same name, so `logo.png` becomes `LogoPng.java` holding `class LogoPng` with constants named `LOGO_PNG`.

### Compression

Compression is optional and runs **before** encryption.

| Option | Description |
| --- | --- |
| `-z`, `--compress <codec>` | Select `deflate` or `zstd`. |
| `--level <n>` | Set the codec level. DEFLATE accepts 1–9 (default 9); Zstandard accepts 1–22 (default 19). Requires `--compress`. |

DEFLATE output is a raw RFC 1951 stream. Zstandard output is a Zstandard frame that declares its content size. Compression is not guaranteed to make every input smaller; already-compressed formats such as PNG, JPEG, ZIP, MP3, and MP4 may grow slightly. The tool reports when compression does not reduce the payload.

### Encryption

Encryption is optional. Pass a 64-character hexadecimal AES-256 key:

| Option | Description |
| --- | --- |
| `-ctr`, `--aes256ctr <key>` | AES-256-CTR. |
| `-cbc`, `--aes256cbc <key>` | AES-256-CBC with PKCS#7 padding. |
| `-iv 0` | Keep the IV in the source comment only. |
| `-iv 1` | Store the 16-byte IV at the start of the array. This is the default. |

The two cipher modes are mutually exclusive. The IV option only applies when encryption is enabled. Each run generates a fresh random IV.

> **Security note:** The generated source comment includes both the IV and the key, as well as the selected cipher. These options are useful for reversible encoding or reducing plaintext visibility in the generated array; they do **not** protect secrets from someone who can read the generated source. CTR and CBC also do not authenticate the data. Do not use this feature as a substitute for secure secret storage or authenticated encryption.

### Base64 output

`-b64`, `--base64` writes the payload as Base64 text instead of byte literals or a hexadecimal string:

```sh
bin2source -f assets/logo.png -python --base64 -o generated/
```

Base64 is applied after compression and encryption. The generated constant is named with a `_b64` suffix, and its length constant describes the decoded payload size. The consuming application must Base64-decode it before decrypting or decompressing.

### Java notes

Java is the only target that turns the payload into a class rather than a set of
module-level constants, and the only one where the compiler refuses a
declaration outright because it carries too much data:

- **The byte array path stops at 8,192 bytes.** javac builds
  `new byte[] { ... }` one instruction per element inside `<clinit>`, and no
  method may exceed 65,535 bytes of bytecode. Measured ceiling on JDK 25:
  9,416 elements (6.96 bytes each). The tool caps at 8,192 to stay clear of it
  and tells you to use `-b64`.
- **The Base64 path changes the constant's type at 49,134 bytes.** A single
  Java string constant cannot hold more than 65,534 characters, so up to that
  payload size the constant is a `String`, and beyond it a `String[]`. Consumer
  code has to match the shape — see
  [`doc/output-format.md`](doc/output-format.md#java-的-base64-有两种形态消费端必读).
- **Nothing modern is required.** The generated source compiles unchanged with
  `--release 8` through `--release 25` (verified on Temurin 25.0.5), because it
  uses plain string literals instead of text blocks.
- **Decoders are mostly in the JDK**: Base64, AES (CTR/CBC) and raw DEFLATE are
  standard library; only Zstandard needs a third-party library such as
  `com.github.luben:zstd-jni`.

## 📦 Generated data and metadata

The generated file contains the payload and constants for its length and SHA-256 digest. When compression is enabled, it also contains the original input length and a digest of the compressed bytes. When encryption is enabled, it contains a digest of the bytes stored in the array; with `-iv 1`, that digest includes the leading IV.

The transformation order is:

```text
input file → compression (optional) → encryption (optional) → Base64 text (optional)
```

To recover the original bytes, reverse the enabled steps: Base64-decode, decrypt, then decompress. The tool generates the data and metadata, but does not generate decryption or decompression functions. The required decoder depends on the options selected and the target language.

Generated source uses ASCII text (a valid UTF-8 subset), regardless of the input file's contents.