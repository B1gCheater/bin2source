# bin2source

把任意文件转成二进制数组，直接编译进 C / C++ / Go / Rust / Python 源码里，
用来替代资源文件。可选先压缩（裸 DEFLATE 或 Zstandard）再 AES-256 加密，让数组
更小，也让明文不那么容易被 `strings` 扫出来。还能把最终字节写成 base64 文本
（`-b64`），源码体积缩到约四分之一。

纯 Go 实现，无 cgo，`CGO_ENABLED=0` 可直接交叉编译三平台单文件。标准库负责大部分
工作：DEFLATE 用 `compress/flate`，AES 用 `crypto/aes`，SHA-256 用 `crypto/sha256`。

第三方依赖只有两个，都是纯 Go：

| 依赖 | 用来做什么 |
| --- | --- |
| [`klauspost/compress`](https://github.com/klauspost/compress) | Zstandard 压缩（标准库没有 zstd 编码器） |
| [`fatih/color`](https://github.com/fatih/color) | 控制台彩色输出 |

消费端解压用 zlib / libzstd / `flate2` / Python 的 `zlib` 或 `zstandard`，
五个语言都是现成的。

## 构建

```sh
./build.sh                    # -> bin/bin2source.exe
./build.sh linux amd64        # -> bin/bin2source_linux_amd64
./build.sh darwin arm64       # -> bin/bin2source_darwin_arm64
```

## 用法

```sh
bin2source -f <文件> <-c|-cpp|-go|-rust|-python> [选项]
```

### 必填

| 参数 | 说明 |
| --- | --- |
| `-f`, `--file` | 输入文件，只能一个 |
| `-c` | 生成 C 头文件 `.h` |
| `-cpp` | 生成 C++ 头文件 `.hpp` |
| `-go` | 生成 Go 源文件 `.go` |
| `-rust` | 生成 Rust 源文件 `.rs` |
| `-python` | 生成 Python 模块 `.py` |

语言必须**恰好指定一个**。

### 压缩（可选，在加密之前）

| 参数 | 说明 |
| --- | --- |
| `-z`, `--compress <codec>` | 压缩算法：`deflate`（裸 DEFLATE，RFC 1951）或 `zstd`（Zstandard，RFC 8478） |
| `--level <n>` | 压缩级别。**每个算法有自己的范围**：`deflate` 1–9（默认 **9**），`zstd` 1–22（默认 **19**） |

`--compress` 必须给算法名，没有默认值可以退：`--compress zstd`，不能只写
`--compress`。写错的两种方式都会被点到——放在末尾的裸 `--compress` 由参数解析
器拒掉，而 `--compress --level 9` 会把 `--level` 当成算法名吞下去，工具会直接
说明是算法名缺失，不让你去怀疑那个多出来的 `9`。`--level` 单独给也报错（和
`-iv` 的规则一样）。将来再加算法就是再加一个名字，是纯增量。

两个算法怎么选，看下面这张表就够了。数字都是 `deflate -9` 与 `zstd -19` 在本机
实跑出来的：

| 输入 | 原始 | deflate -9 | zstd -19 | 谁赢 |
| --- | --- | --- | --- | --- |
| `bin2source.exe` | 2,815,488 | 1,234,679（43.9%） | **1,174,839（41.7%）** | zstd，但只差 2 个点 |
| 1 MB 重复 JSON | 960,000 | 4,896（0.5%） | **327（0.03%）** | **zstd，小 15 倍** |
| 随机 200 KB | 200,000 | 200,037 | **200,019** | zstd，膨胀更少 |
| 空文件 | 0 | **2** | 9 | deflate |
| 2 字节 | 2 | **5** | 15 | deflate |

第二行是重点，也是 zstd 存在的理由：**DEFLATE 的匹配窗口上限是 32 KB**（RFC 1951
规定的），距离超过 32 KB 的重复它根本看不见。zstd 的窗口大得多，所以任何"结构在
大范围内重复"的文件——固件镜像、多语言资源包、规模化的 JSON/日志、带重复段的可执行
体——它能压掉 deflate 压不掉的部分。第一行则说明另一件事：如果数据没有长距离重复，
两者的差距就收窄到两三个百分点。

反过来，越小的资源 deflate 越划算：zstd 的 frame 光是声明自己就要 9 字节起步
（寻常小文件 13 字节上下），deflate 空输入只要 2 字节，上面最后两行就是这个
差价的体现。

**所以：几百 KB 以上、结构有重复，用 `zstd`；小资源、图标、几 KB 的配置，用
`deflate`。**

另外一个容易误会、其实不用担心的地方：`--level` 只影响编码器找匹配花多大力气，
**不影响解码端**——生成的 zstd frame 一定声明内容大小（single segment 形态，
声明的窗口就是内容本身），所以解码准备的内存只跟资源大小成正比。2 KB 的资源用
22 级压，解码也就是 2 KB 的量级；Python `zstandard` 的一次性解压接口也因此可以
直接使用。

还有个实测现象值得知道：zstd 在本实现（`klauspost/compress`）里 **15 级以上收益饱和**，
15、19、20、21、22 产出的字节完全相同。范围仍然按官方的 1–22 开放，好让 `--level`
的语义和 zstd 文档一致，但你调 22 不会比 19 更小。

**压缩一定在加密之前**，这个顺序不可交换：AES 的输出是等概率密文，熵接近
最大，通用压缩器对它基本无效（实测把已经 deflate 过的数据再 deflate 一次只
省 0.3%，对密文只会更糟）。所以工具把它固化成一条流水线，而不是两个可以随意
勾选的步骤。

压缩**不是**默认开的，因为很多资源本来就已经压过了。**已经压过的格式不要开**：
PNG / JPEG / zip / mp3 / mp4 / 已经是 zstd 或 xz 的东西，两种算法都只能挤出
0~3%，付了消费端一个解码库却几乎什么都没省。真正的随机数据反而会变大几十字节，
因为压缩格式至少要几个字节说明"这一块没有压缩"。

靠扩展名猜不出来，所以工具**不会**自作主张地跳过压缩。你要了算法，数组里就是
那个算法的输出；没变小时只在 stderr 打一行提示：

```
bin2source: warning: deflate gained nothing: 200000 -> 200037 bytes (already compressed input?)
bin2source: warning: zstd gained nothing: 200000 -> 200019 bytes (already compressed input?)
```

看到这行就说明这个文件不该加 `--compress`，去掉重新生成即可。

### zstd 的依赖

`deflate` 用的是 Go 标准库，`zstd` 用的是
[`klauspost/compress`](https://github.com/klauspost/compress)。选它是因为它是纯 Go、
没有 cgo，所以 `CGO_ENABLED=0` 交叉编译和"一个静态二进制丢到任何平台"这两件事都还
成立，Linux / macOS / Windows 和 amd64 / arm64 都跑过。

代价要说清楚：这是本项目**第一个第三方依赖**，加上它之后工具自身的二进制从
2,152,448 涨到 2,479,104 字节（+15%）；后来为控制台彩色输出引入 `fatih/color`，
又涨到 2,800,128 字节。两者都是纯 Go、无 cgo，交叉编译那条路没受影响，
但这不影响生成出来的数组——只是你分发 exe 时知道一下它为什么这么大。

### 加密（可选，AES-256）

| 参数 | 说明 |
| --- | --- |
| `-ctr`, `--aes256ctr` | AES-256-CTR，值是 64 个 hex 字符，不要 `0x` 前缀 |
| `-cbc`, `--aes256cbc` | AES-256-CBC + PKCS#7，同上 |

两个互斥，都不给就是明文数组。给了加密就必须给够 64 个 hex 字符的密钥。
密钥输入容忍 `0x` 前缀和大小写，但生成的文件里一律输出小写。

| 参数 | 说明 |
| --- | --- |
| `-iv 0` | IV 只出现在源码注释里，数组里没有 IV |
| `-iv 1` | IV 作为数组的前 16 字节（**默认**） |

`-iv` 只在同时给了 `-ctr` 或 `-cbc` 时有意义，单独给会报错。

### 输出

| 参数 | 说明 |
| --- | --- |
| `-o`, `--output` | 输出文件，或一个目录（目录则保留默认文件名）。默认 `./<名字>.<后缀>` |
| `-name` | 手动指定数组标识符，覆盖从文件名推导的结果 |
| `-pkg` | Go 的 package 名（默认 `main`）；Rust 的 mod 名（默认不包 mod） |
| `-b64`, `--base64` | 把载荷写成 base64 文本，而不是 `0xNN,` 数组或十六进制字符串，见下节 |

### base64 文本模式（`-b64`）

数组是每个字节六个字符（`0xNN,` 加分隔），base64 是每三个字节四个字符，实测：

| 语言 | 默认写法 | `-b64` | 缩小 |
| --- | --- | --- | --- |
| C / C++ | 16,947,318 | 3,962,218 | 4.28× |
| Go | 16,947,193 | 4,061,118 | 4.17× |
| Rust | 16,947,221 | 3,912,584 | 4.33× |
| Python | 6,262,986 | 4,110,621 | 1.52× |

（2.8 MB 的 `bin2source.exe` 本机实测，单位字节。Python 本来就用的十六进制
字符串，所以只缩到约 2/3。）

顺序上 base64 是**最后一步**：先压缩、再加密、最后才编码成文本。所以字节本身没
变，`x_len` 仍是解码后的字节数，三个摘要的语义也不变——只是源码里多了“先
base64 解码”这一步。名字带 `_b64` 后缀（`data_b64` / `DataB64` / `DATA_B64`），
注释块里有一行 `base64  : RFC 4648 standard, padded` 声明。完整契约和各语言
解码代码见 [output-format.md](output-format.md#base64-文本模式)。

Go 和 Rust 的数组长度本来是 `len(X)` / `.len()` 推导出来的，base64 模式下没有
数组可测，这两个常量改成字面量（`const XLen = 3738` / `pub const X_LEN: usize = 3738;`）。

**什么时候用**：几百 KB 以上、又希望源码别爆炸（或让编译器少啃几百万个
`0xNN,` token）的时候。**什么时候不用**：追求编译产物最小或零运行时开销
（比如 MCU 上直接跑 flash 里的数组）——base64 文本本身仍在二进制里
（约 1.33×），运行时还得解出一份。

### 控制台输出

所有信息走 **stderr**，不污染 stdout，所以把生成结果重定向到文件是安全的。

工具会给这些信息上色，方便一眼分清哪些是报错、哪些成功了：

| 颜色 | 什么信息 |
| --- | --- |
| 浅红 | 出错，程序会以非 0 退出 |
| 浅黄 | 警告：跑通了，但有件事值得你知道（比如压缩后没变小） |
| 浅绿 | 成功 |
| 浅蓝 | 帮助文本（`-h`） |

颜色只在 **stderr 连着终端**时才输出。重定向到文件或管道会自动关掉，
不会在你的日志里留下一堆乱码转义序列。也可以显式关掉：

```sh
NO_COLOR=1 bin2source -f logo.png -c      # Linux / macOS
$env:NO_COLOR=1; bin2source -f logo.png -c # PowerShell
```

Windows 这边由 `go-colorable` 处理：现代终端（Windows Terminal、Windows 10+
的控制台）直接吃 ANSI 转义序列，老终端则退回控制台 API，两边都有颜色。

## 例子

```sh
# 明文 C 头文件 -> ./logo_png.h
bin2source -f assets/logo.png -c

# 先压缩，不加密
bin2source -f big.json -go --compress deflate

# 压缩 + AES-256-CTR，IV 放在数组前 16 字节
bin2source -f assets/logo.png -rust --compress deflate -ctr a1b2...8f90 -o out/

# 只加密，不压缩（适合本来就是压缩格式的资源）
bin2source -f assets/photo.jpg -python -cbc a1b2...8f90 -iv 0

# 想要更小的输出就调级别（默认已经是 9）
bin2source -f data.bin -cpp --compress deflate --level 9

# 文件名是中文，手动指定标识符
bin2source -f 图标.png -go -name icon -pkg assets

# 输出到嵌套目录（会自动创建）
bin2source -f data.bin -cpp -o build/gen/assets/
```

## 标识符怎么来的

从**输出文件名**推导；如果没给 `-o` 或者 `-o` 是目录，则从**输入文件名**推导。
先去掉对应语言的扩展名，再做清洗：

| 输入 | 清洗后 | 说明 |
| --- | --- | --- |
| `logo.png` | `logo_png` | 非字母数字变成下划线，全小写 |
| `my-logo v2.png` | `my_logo_v2_png` | 连续分隔符合并成一个 |
| `1logo.png` | `_1logo_png` | 数字开头补下划线（五种语言都不允许数字开头） |
| `图标.png` | `png` | 非 ASCII 被当成分隔符，同时打印警告 |

五种语言各自的命名惯例：

| 语言 | 数组名 | 长度常量 | 长度怎么表示 | SHA-256 常量 |
| --- | --- | --- | --- | --- |
| C | `logo_png` | `logo_png_len` | 字面量 `1047` | `logo_png_sha256` |
| C++ | `logo_png` | `logo_png_len` | 字面量 `1047` | `logo_png_sha256` |
| Go | `LogoPng` | `LogoPngLen` | `len(LogoPng)` | `LogoPngSha256` |
| Rust | `LOGO_PNG` | `LOGO_PNG_LEN` | `LOGO_PNG.len()` | `LOGO_PNG_SHA256` |
| Python | `LOGO_PNG` | `LOGO_PNG_LEN` | `len(LOGO_PNG)` | `LOGO_PNG_SHA256` |

C 和 C++ 用字面量而不是 `sizeof`，是因为这两个语言没有运行时求长的习惯写法；
Go / Rust / Python 的长度常量从数组本身推导，永远不会和数组对不上。

给了 `--compress` 时会多一个记录**原始长度**的常量（`logo_png_raw_len` /
`LogoPngRawLen` / `LOGO_PNG_RAW_LEN`）。它是唯一无法从数组推导出来的数字：
裸 DEFLATE 流不自带长度，而 C 和 C++ 必须先分配好缓冲区才能解压。
详见 [output-format.md](output-format.md#长度常量)。

## SHA-256 校验值

从输入文件到数组，字节被改了三道，所以**每道边界都有一个常量**。作用是在
回读失败时定位到是哪一步出的问题，而不是只知道"对不上"。

| 常量 | 覆盖什么 | 用途 |
| --- | --- | --- |
| `logo_png_sha256` | 输入文件（压缩、加密之前） | 最终比对 |
| `logo_png_deflate_sha256` | 压缩输出（加密之前） | 解密后比对，确认密钥没错 |
| `logo_png_array_sha256` | 数组里的字面字节 | **不需要密钥**就能验，确认数组没被改坏 |

出现规则由参数决定：`sha256` 永远有；`deflate_sha256` 当且仅当给了
`--compress`；`array_sha256` 当且仅当给了 `-ctr` / `-cbc`。值都是小写 hex，
和不加任何参数的 `sha256sum` 输出完全一致：

```c
static const char logo_png_sha256[] = (
	"bdc1d96765535b0fbcadbf9ad530cebb63ba40890a5ce92b81eb481be20426cf"
);
```

`logo_png_sha256` 永远是**转换前**那个文件的哈希，不会因为加了压缩或加密而
变化。所以消费端的流程是：解密 → 解压 → 算一次 SHA-256，应该正好等于它。

`array_sha256` 是三者里最实用的一条：它覆盖数组里那串字面量的全部字节
（`-iv 1` 时包含前 16 字节 IV），因此**不需要密钥**就能验证数组本身没被改过。

```go
// 不需要密钥，拿到源码就能跑
sum := sha256.Sum256(LogoPng)
if hex.EncodeToString(sum[:]) != LogoPngArraySha256 {
    log.Fatal("数组被改坏了")
}
```

各语言的常量名、类型和写法见
[output-format.md](output-format.md#三级-sha-256)。

非 ASCII 文件名清洗后如果什么都不剩（比如 `图标` 没有扩展名），会直接报错并
提示用 `-name`，不会瞎猜一个名字。

## 生成文件长什么样

见 [output-format.md](output-format.md)。

## 验证

```sh
go test ./src/...                          # 单元测试，含 NIST AES 向量
python test/roundtrip.py test/logo.png     # 回环测试：生成 -> 编译 -> 跑 -> 比对
python test/roundtrip.py test/logo.png --compress deflate
python test/roundtrip.py test/logo.png --compress deflate --cbc <64位hex> --iv 0
python test/roundtrip.py test/logo.png --cbc <64位hex> --iv 0
python test/roundtrip.py test/logo.png --b64
python test/roundtrip.py test/logo.png --b64 --compress zstd --cbc <64位hex>
```

回环测试会把每种语言生成的文件真的编译执行一遍，把数组字节 dump 出来，然后
**按解码顺序一站一站走回去**，每一站都比对该站的摘要常量：

```
数组 bytes  -> array_sha256     （不需要密钥）
解密        -> deflate_sha256
inflate     -> sha256（输入文件）
```

所以对不上时报的是"哪一步错了"，而不是笼统的"不匹配"。加密用 Python 的
`cryptography`（OpenSSL）独立解一遍，解压用 Python 标准库的
`zlib.decompressobj(-15)` 独立解一遍——加密和压缩两个格式都是跟另一套实现
交叉验证过的，不是自己验自己。

那些摘要常量也不是生成器自己说对就对：这一步会把每个语言的常量从源码里读回来
（Python 那份用 `ast` 解析生成出来的模块，其余四种从源码文本里取字面量），
和 `hashlib` 算出的真实哈希逐一比对。`raw_len` 也一并核对。

没有装 C/C++ 编译器时，这两个语言会退化成用 `pycparser` 做语法校验，并在结果里
标成 `PARSE` 而不是 `OK`——能发现语法和元素个数错误，但不等于真的编译过。
`PARSE` / `SKIP` 的情况下摘要常量和 `raw_len` 仍然会被检查，因为它们不需要编译。

## 加密的实际强度

**这是混淆，不是安全机制。** 密钥和 IV 都明文写在生成的源码里，CTR 模式还可以
被篡改且没有完整性校验。它挡住的是 `strings`、`grep` 和肉眼，不是有意为之的
攻击者。要真安全请用正经的密钥管理和 AEAD。

压缩同样不是安全手段：它只是让数组更小，`--compress` 之后明文该能被还原还是
能被还原。真要保密，改动不在算法而在**架构**——密钥不能出现在生成的文件里，
得运行时从环境变量或用户输入提供。现在还没有这个模式。
