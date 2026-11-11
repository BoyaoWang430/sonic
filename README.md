# Sonic

English | [中文](README_ZH_CN.md)

A blazingly fast JSON serializing &amp; deserializing library, accelerated by SIMD (single-instruction-multiple-data) and architecture-specialized native kernels.

## Requirement

- Go: 1.20~1.27
- OS: Linux / MacOS / Windows
- CPU: AMD64 / ARM64 / RISC-V (RVA22 / RVA23)

## Features

- Runtime object binding without code generation
- Complete APIs for JSON value manipulation
- Fast, fast, fast!

## APIs

see [go.dev](https://pkg.go.dev/github.com/bytedance/sonic)

## Benchmarks

For **all sizes** of json and **all scenarios** of usage, **Sonic performs best**.

> Note: `Sonic_Next` / `sonic-next` denotes this refactored multi-arch branch (zero `//go:linkname`, zero JIT, native Go SIMD scanners + VM), while `Sonic` / `sonic` denotes upstream `main` (`4640e45`, built with `-ldflags=-checklinkname=0` on Go 1.27; on `riscv64`, upstream `main` falls back to `encoding/json` for Marshal/Unmarshal).

- [Medium](https://github.com/bytedance/sonic/blob/main/decoder/testdata_test.go#L19) (13KB, 300+ key, 6 layers)

```powershell
goversion: 1.27.0
goos: linux
goarch: amd64
cpu: Intel(R) Xeon(R) Platinum 8336C CPU @ 2.30GHz
BenchmarkEncoder_Generic_Sonic_Next-16                         42449 ns/op        307.08 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic_Next_Fast-16                    23884 ns/op        545.75 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic-16                              72160 ns/op        180.64 MB/s       13140 B/op         40 allocs/op
BenchmarkEncoder_Generic_Sonic_Fast-16                         39354 ns/op        331.22 MB/s       13139 B/op         40 allocs/op
BenchmarkEncoder_Generic_JsonIter-16                           58409 ns/op        223.17 MB/s       17984 B/op        153 allocs/op
BenchmarkEncoder_Generic_GoJson-16                            101631 ns/op        128.26 MB/s       26873 B/op         55 allocs/op
BenchmarkEncoder_Generic_StdLib-16                             67660 ns/op        192.65 MB/s        9576 B/op          8 allocs/op
BenchmarkEncoder_Binding_Sonic_Next-16                          6287 ns/op       2073.46 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic_Next_Fast-16                     5863 ns/op       2223.32 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic-16                               8522 ns/op       1529.60 MB/s        9489 B/op          2 allocs/op
BenchmarkEncoder_Binding_Sonic_Fast-16                          7504 ns/op       1737.14 MB/s        9489 B/op          2 allocs/op
BenchmarkEncoder_Binding_JsonIter-16                           26228 ns/op        496.99 MB/s        9480 B/op          2 allocs/op
BenchmarkEncoder_Binding_GoJson-16                             10790 ns/op       1208.12 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_StdLib-16                             29242 ns/op        445.76 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next-16                 7334 ns/op       1777.40 MB/s        9636 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next_Fast-16            5831 ns/op       2235.46 MB/s        9525 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic-16                      9926 ns/op       1313.22 MB/s       14010 B/op         40 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Fast-16                 7660 ns/op       1701.63 MB/s       13862 B/op         40 allocs/op
BenchmarkEncoder_Parallel_Generic_JsonIter-16                   5859 ns/op       2224.94 MB/s       18007 B/op        153 allocs/op
BenchmarkEncoder_Parallel_Generic_GoJson-16                    11613 ns/op       1122.49 MB/s       26885 B/op         55 allocs/op
BenchmarkEncoder_Parallel_Generic_StdLib-16                     6087 ns/op       2141.52 MB/s        9605 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next-16                 4281 ns/op       3044.74 MB/s        9508 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next_Fast-16            4237 ns/op       3076.55 MB/s        9510 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic-16                      2886 ns/op       4517.18 MB/s       11510 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Fast-16                 2726 ns/op       4781.62 MB/s       11380 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_JsonIter-16                   2579 ns/op       5055.05 MB/s        9490 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_GoJson-16                     1491 ns/op       8740.26 MB/s        9480 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_StdLib-16                     2575 ns/op       5061.45 MB/s        9515 B/op          3 allocs/op

BenchmarkDecoder_Generic_Sonic_Next-16                         41592 ns/op        313.40 MB/s       35857 B/op        237 allocs/op
BenchmarkDecoder_Generic_Sonic_Next_Fast-16                    35673 ns/op        365.41 MB/s       27889 B/op        236 allocs/op
BenchmarkDecoder_Generic_Sonic-16                              88646 ns/op        147.04 MB/s       55323 B/op        747 allocs/op
BenchmarkDecoder_Generic_Sonic_Fast-16                         74455 ns/op        175.07 MB/s       47562 B/op        337 allocs/op
BenchmarkDecoder_Generic_StdLib-16                            200069 ns/op         65.15 MB/s       49368 B/op        931 allocs/op
BenchmarkDecoder_Generic_JsonIter-16                          125530 ns/op        103.84 MB/s       54216 B/op       1085 allocs/op
BenchmarkDecoder_Generic_GoJson-16                            121880 ns/op        106.95 MB/s       64865 B/op        996 allocs/op
BenchmarkDecoder_Binding_Sonic_Next-16                         16653 ns/op        782.75 MB/s        7840 B/op         24 allocs/op
BenchmarkDecoder_Binding_Sonic_Next_Fast-16                    14415 ns/op        904.25 MB/s        4480 B/op         22 allocs/op
BenchmarkDecoder_Binding_Sonic-16                              26373 ns/op        494.26 MB/s        9608 B/op        127 allocs/op
BenchmarkDecoder_Binding_Sonic_Fast-16                         21374 ns/op        609.86 MB/s        6576 B/op         24 allocs/op
BenchmarkDecoder_Binding_StdLib-16                             68878 ns/op        189.25 MB/s        8912 B/op         70 allocs/op
BenchmarkDecoder_Binding_JsonIter-16                           52334 ns/op        249.08 MB/s       14536 B/op        379 allocs/op
BenchmarkDecoder_Binding_GoJson-16                             41006 ns/op        317.88 MB/s       21937 B/op         49 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next-16                16535 ns/op        788.31 MB/s       36243 B/op        237 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next_Fast-16           13374 ns/op        974.63 MB/s       28153 B/op        236 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic-16                     29501 ns/op        441.86 MB/s       55777 B/op        747 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Fast-16                22557 ns/op        577.88 MB/s       47943 B/op        337 allocs/op
BenchmarkDecoder_Parallel_Generic_StdLib-16                    20381 ns/op        639.58 MB/s       49389 B/op        931 allocs/op
BenchmarkDecoder_Parallel_Generic_JsonIter-16                  17348 ns/op        751.38 MB/s       54223 B/op       1085 allocs/op
BenchmarkDecoder_Parallel_Generic_GoJson-16                    18140 ns/op        718.58 MB/s       64890 B/op        996 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next-16                 3552 ns/op       3669.51 MB/s        7974 B/op         24 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next_Fast-16            2045 ns/op       6375.31 MB/s        4630 B/op         22 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic-16                      4571 ns/op       2851.56 MB/s        9725 B/op        127 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Fast-16                 2853 ns/op       4568.25 MB/s        6712 B/op         24 allocs/op
BenchmarkDecoder_Parallel_Binding_StdLib-16                     5111 ns/op       2550.19 MB/s        8917 B/op         70 allocs/op
BenchmarkDecoder_Parallel_Binding_JsonIter-16                   4946 ns/op       2635.50 MB/s       14538 B/op        379 allocs/op
BenchmarkDecoder_Parallel_Binding_GoJson-16                     5003 ns/op       2605.30 MB/s       21980 B/op         49 allocs/op

BenchmarkGetOne_Sonic_Next-16                                   1180 ns/op      11048.43 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Sonic-16                                        1285 ns/op      10147.84 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Gjson-16                                        9140 ns/op       1426.13 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Jsoniter-16                                    77763 ns/op        167.63 MB/s       27664 B/op        635 allocs/op
BenchmarkGetOne_Parallel_Sonic_Next-16                         75.34 ns/op     173026.36 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Sonic-16                              87.66 ns/op     148698.57 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Gjson-16                              539.2 ns/op      24175.09 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Jsoniter-16                            7126 ns/op       1829.32 MB/s       27666 B/op        635 allocs/op
BenchmarkSetOne_Sonic_Next-16                                   4139 ns/op       3149.17 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Sonic-16                                        4977 ns/op       2619.07 MB/s        1056 B/op          2 allocs/op
BenchmarkSetOne_Sjson-16                                       49357 ns/op        264.10 MB/s       52152 B/op         10 allocs/op
BenchmarkSetOne_Jsoniter-16                                   115856 ns/op        112.51 MB/s       45120 B/op        952 allocs/op
BenchmarkSetOne_Parallel_Sonic_Next-16                         830.5 ns/op      15696.08 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Parallel_Sonic-16                              712.8 ns/op      18285.90 MB/s        1168 B/op          2 allocs/op
BenchmarkSetOne_Parallel_Sjson-16                               8445 ns/op       1543.51 MB/s       52166 B/op         10 allocs/op
BenchmarkSetOne_Parallel_Jsoniter-16                           11964 ns/op       1089.55 MB/s       45125 B/op        952 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic_Next-16                       4452 ns/op       2928.15 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic-16                            6869 ns/op       1897.69 MB/s        2944 B/op         12 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic_Next-16              1394 ns/op       9353.84 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic-16                   1765 ns/op       7384.01 MB/s        3205 B/op         12 allocs/op
BenchmarkLoadNode/Interface()_Sonic_Next-16                    16403 ns/op        794.67 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode/Interface()_Sonic-16                         29200 ns/op        446.41 MB/s       23393 B/op         95 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic_Next-16            7072 ns/op       1843.25 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic-16                12058 ns/op       1081.06 MB/s       24807 B/op         95 allocs/op
```

<details>
<summary>More architectures — ARM64 / RISC-V RVA22 / RISC-V RVA23 (Medium 13KB)</summary>

```powershell
goversion: 1.27.0
goos: linux
goarch: arm64
cpu: ARM64 Neoverse (aarch64)
BenchmarkEncoder_Generic_Sonic_Next-16                         33484 ns/op        389.29 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic_Next_Fast-16                    20572 ns/op        633.62 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic-16                              75420 ns/op        172.83 MB/s       13140 B/op         40 allocs/op
BenchmarkEncoder_Generic_Sonic_Fast-16                         50701 ns/op        257.09 MB/s       13138 B/op         40 allocs/op
BenchmarkEncoder_Generic_JsonIter-16                           47678 ns/op        273.40 MB/s       17984 B/op        153 allocs/op
BenchmarkEncoder_Generic_GoJson-16                             91085 ns/op        143.11 MB/s       26792 B/op         54 allocs/op
BenchmarkEncoder_Generic_StdLib-16                             52174 ns/op        249.84 MB/s        9576 B/op          8 allocs/op
BenchmarkEncoder_Binding_Sonic_Next-16                          5781 ns/op       2254.62 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic_Next_Fast-16                     5117 ns/op       2547.33 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic-16                              16457 ns/op        792.07 MB/s        9489 B/op          2 allocs/op
BenchmarkEncoder_Binding_Sonic_Fast-16                         14781 ns/op        881.86 MB/s        9488 B/op          2 allocs/op
BenchmarkEncoder_Binding_JsonIter-16                           23157 ns/op        562.88 MB/s        9480 B/op          2 allocs/op
BenchmarkEncoder_Binding_GoJson-16                             10757 ns/op       1211.72 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_StdLib-16                             26485 ns/op        492.17 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next-16                 2864 ns/op       4550.96 MB/s        9535 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next_Fast-16            2115 ns/op       6164.51 MB/s        9488 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic-16                     11199 ns/op       1163.97 MB/s       14022 B/op         40 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Fast-16                 9806 ns/op       1329.29 MB/s       13700 B/op         40 allocs/op
BenchmarkEncoder_Parallel_Generic_JsonIter-16                   4630 ns/op       2815.51 MB/s       17987 B/op        153 allocs/op
BenchmarkEncoder_Parallel_Generic_GoJson-16                     8740 ns/op       1491.39 MB/s       26746 B/op         55 allocs/op
BenchmarkEncoder_Parallel_Generic_StdLib-16                     4503 ns/op       2894.42 MB/s        9580 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next-16                982.4 ns/op      13268.81 MB/s        9476 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next_Fast-16           999.1 ns/op      13047.08 MB/s        9476 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic-16                      3204 ns/op       4068.91 MB/s       10846 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Fast-16                 2910 ns/op       4478.95 MB/s       10671 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_JsonIter-16                   1868 ns/op       6978.15 MB/s        9481 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_GoJson-16                     1213 ns/op      10746.40 MB/s        9473 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_StdLib-16                     2216 ns/op       5881.50 MB/s        9505 B/op          3 allocs/op

BenchmarkDecoder_Generic_Sonic_Next-16                         35750 ns/op        364.61 MB/s       35857 B/op        237 allocs/op
BenchmarkDecoder_Generic_Sonic_Next_Fast-16                    31243 ns/op        417.21 MB/s       27889 B/op        236 allocs/op
BenchmarkDecoder_Generic_Sonic-16                              45982 ns/op        283.48 MB/s       36200 B/op        705 allocs/op
BenchmarkDecoder_Generic_Sonic_Fast-16                         30217 ns/op        431.37 MB/s       28952 B/op        105 allocs/op
BenchmarkDecoder_Generic_StdLib-16                            157392 ns/op         82.82 MB/s       49368 B/op        931 allocs/op
BenchmarkDecoder_Generic_JsonIter-16                           99963 ns/op        130.40 MB/s       54216 B/op       1085 allocs/op
BenchmarkDecoder_Generic_GoJson-16                            100686 ns/op        129.46 MB/s       64865 B/op        996 allocs/op
BenchmarkDecoder_Binding_Sonic_Next-16                         14214 ns/op        917.04 MB/s        7840 B/op         24 allocs/op
BenchmarkDecoder_Binding_Sonic_Next_Fast-16                    12464 ns/op       1045.78 MB/s        4480 B/op         22 allocs/op
BenchmarkDecoder_Binding_Sonic-16                              24567 ns/op        530.59 MB/s        7952 B/op        144 allocs/op
BenchmarkDecoder_Binding_Sonic_Fast-16                         21153 ns/op        616.23 MB/s        4920 B/op         41 allocs/op
BenchmarkDecoder_Binding_StdLib-16                             57244 ns/op        227.71 MB/s        8912 B/op         70 allocs/op
BenchmarkDecoder_Binding_JsonIter-16                           42977 ns/op        303.30 MB/s       14536 B/op        379 allocs/op
BenchmarkDecoder_Binding_GoJson-16                             32442 ns/op        401.79 MB/s       21936 B/op         49 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next-16                 9756 ns/op       1336.07 MB/s       36029 B/op        237 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next_Fast-16            8137 ns/op       1601.95 MB/s       28032 B/op        236 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic-16                      7020 ns/op       1856.72 MB/s       36553 B/op        705 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Fast-16                 5397 ns/op       2415.43 MB/s       29214 B/op        105 allocs/op
BenchmarkDecoder_Parallel_Generic_StdLib-16                    16348 ns/op        797.36 MB/s       49371 B/op        931 allocs/op
BenchmarkDecoder_Parallel_Generic_JsonIter-16                  15262 ns/op        854.08 MB/s       54220 B/op       1085 allocs/op
BenchmarkDecoder_Parallel_Generic_GoJson-16                    15936 ns/op        817.97 MB/s       64880 B/op        996 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next-16                 2355 ns/op       5534.62 MB/s        7893 B/op         24 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next_Fast-16            1469 ns/op       8872.10 MB/s        4546 B/op         22 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic-16                      2210 ns/op       5897.94 MB/s        8439 B/op        144 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Fast-16                 1774 ns/op       7345.97 MB/s        5305 B/op         41 allocs/op
BenchmarkDecoder_Parallel_Binding_StdLib-16                     4460 ns/op       2922.72 MB/s        8913 B/op         70 allocs/op
BenchmarkDecoder_Parallel_Binding_JsonIter-16                   4369 ns/op       2983.25 MB/s       14537 B/op        379 allocs/op
BenchmarkDecoder_Parallel_Binding_GoJson-16                     4151 ns/op       3140.08 MB/s       21960 B/op         49 allocs/op

BenchmarkGetOne_Sonic_Next-16                                   2767 ns/op       4711.13 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Sonic-16                                        3360 ns/op       3879.15 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Gjson-16                                        6603 ns/op       1974.10 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Jsoniter-16                                    62923 ns/op        207.16 MB/s       27664 B/op        635 allocs/op
BenchmarkGetOne_Parallel_Sonic_Next-16                         172.6 ns/op      75500.75 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Sonic-16                              210.1 ns/op      62054.61 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Gjson-16                              419.0 ns/op      31108.49 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Jsoniter-16                            6740 ns/op       1933.91 MB/s       27665 B/op        635 allocs/op
BenchmarkSetOne_Sonic_Next-16                                   5084 ns/op       2563.99 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Sonic-16                                        7198 ns/op       1810.96 MB/s        1056 B/op          2 allocs/op
BenchmarkSetOne_Sjson-16                                       33841 ns/op        385.18 MB/s       52152 B/op         10 allocs/op
BenchmarkSetOne_Jsoniter-16                                    94257 ns/op        138.29 MB/s       45120 B/op        952 allocs/op
BenchmarkSetOne_Parallel_Sonic_Next-16                         518.7 ns/op      25128.74 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Parallel_Sonic-16                              984.4 ns/op      13241.22 MB/s        1146 B/op          2 allocs/op
BenchmarkSetOne_Parallel_Sjson-16                               6698 ns/op       1946.17 MB/s       52160 B/op         10 allocs/op
BenchmarkSetOne_Parallel_Jsoniter-16                           10665 ns/op       1222.18 MB/s       45123 B/op        952 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic_Next-16                       4124 ns/op       3160.98 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic-16                            7141 ns/op       1825.37 MB/s        2944 B/op         12 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic_Next-16             760.6 ns/op      17137.71 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic-16                   2153 ns/op       6055.71 MB/s        3120 B/op         12 allocs/op
BenchmarkLoadNode/Interface()_Sonic_Next-16                    14217 ns/op        916.83 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode/Interface()_Sonic-16                         25424 ns/op        512.70 MB/s       23392 B/op         95 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic_Next-16            3699 ns/op       3523.87 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic-16                14201 ns/op        917.89 MB/s       24356 B/op         95 allocs/op
```

```powershell
goversion: 1.27.0
goos: linux
goarch: riscv64 (GORISCV64=rva22u64)
cpu: RISC-V 64-bit RVA22U64 (Scalar + Zba/Zbb/Zbs)
BenchmarkEncoder_Generic_Sonic_Next-16                         48313 ns/op        269.80 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic_Next_Fast-16                    31317 ns/op        416.22 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic-16                              72200 ns/op        180.54 MB/s        9577 B/op          8 allocs/op
BenchmarkEncoder_Generic_Sonic_Fast-16                         65391 ns/op        199.34 MB/s        9577 B/op          8 allocs/op
BenchmarkEncoder_Generic_JsonIter-16                           89098 ns/op        146.30 MB/s       17984 B/op        153 allocs/op
BenchmarkEncoder_Generic_GoJson-16                            160136 ns/op         81.40 MB/s       26942 B/op         55 allocs/op
BenchmarkEncoder_Generic_StdLib-16                            183906 ns/op         70.88 MB/s       32881 B/op        653 allocs/op
BenchmarkEncoder_Binding_Sonic_Next-16                          7400 ns/op       1761.44 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic_Next_Fast-16                     6720 ns/op       1939.59 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic-16                              38093 ns/op        342.19 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Binding_Sonic_Fast-16                         38591 ns/op        337.77 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Binding_JsonIter-16                           33504 ns/op        389.06 MB/s        9480 B/op          2 allocs/op
BenchmarkEncoder_Binding_GoJson-16                             16315 ns/op        798.97 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_StdLib-16                             23087 ns/op        564.61 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next-16                 6556 ns/op       1988.36 MB/s        9654 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next_Fast-16            5402 ns/op       2413.16 MB/s        9528 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic-16                      8693 ns/op       1499.54 MB/s        9647 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Fast-16                 9080 ns/op       1435.57 MB/s        9658 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Generic_JsonIter-16                  30850 ns/op        422.52 MB/s       18019 B/op        153 allocs/op
BenchmarkEncoder_Parallel_Generic_GoJson-16                    20277 ns/op        642.83 MB/s       26891 B/op         55 allocs/op
BenchmarkEncoder_Parallel_Generic_StdLib-16                    36488 ns/op        357.24 MB/s       32950 B/op        653 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next-16                 3668 ns/op       3554.06 MB/s        9495 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next_Fast-16            3723 ns/op       3501.24 MB/s        9497 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic-16                      5734 ns/op       2273.15 MB/s        9525 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Fast-16                 5903 ns/op       2208.02 MB/s        9519 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Binding_JsonIter-16                   5605 ns/op       2325.45 MB/s        9492 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_GoJson-16                     5342 ns/op       2440.31 MB/s        9485 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_StdLib-16                     4592 ns/op       2838.87 MB/s        9483 B/op          1 allocs/op

BenchmarkDecoder_Generic_Sonic_Next-16                         56712 ns/op        229.85 MB/s       35857 B/op        237 allocs/op
BenchmarkDecoder_Generic_Sonic_Next_Fast-16                    46800 ns/op        278.53 MB/s       27889 B/op        236 allocs/op
BenchmarkDecoder_Generic_Sonic-16                             245728 ns/op         53.05 MB/s       96076 B/op        946 allocs/op
BenchmarkDecoder_Generic_Sonic_Fast-16                        244845 ns/op         53.24 MB/s       96076 B/op        946 allocs/op
BenchmarkDecoder_Generic_StdLib-16                            221650 ns/op         58.81 MB/s       49464 B/op        795 allocs/op
BenchmarkDecoder_Generic_JsonIter-16                          168584 ns/op         77.32 MB/s       54218 B/op       1085 allocs/op
BenchmarkDecoder_Generic_GoJson-16                            162257 ns/op         80.34 MB/s       64876 B/op        996 allocs/op
BenchmarkDecoder_Binding_Sonic_Next-16                         20881 ns/op        624.25 MB/s        7840 B/op         24 allocs/op
BenchmarkDecoder_Binding_Sonic_Next_Fast-16                    17817 ns/op        731.59 MB/s        4480 B/op         22 allocs/op
BenchmarkDecoder_Binding_Sonic-16                             104104 ns/op        125.21 MB/s       55618 B/op         85 allocs/op
BenchmarkDecoder_Binding_Sonic_Fast-16                        104094 ns/op        125.22 MB/s       55618 B/op         85 allocs/op
BenchmarkDecoder_Binding_StdLib-16                            197817 ns/op         65.89 MB/s       11416 B/op        160 allocs/op
BenchmarkDecoder_Binding_JsonIter-16                           73835 ns/op        176.54 MB/s       14536 B/op        379 allocs/op
BenchmarkDecoder_Binding_GoJson-16                             48200 ns/op        270.43 MB/s       21943 B/op         49 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next-16                12970 ns/op       1004.97 MB/s       36117 B/op        237 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next_Fast-16           10177 ns/op       1280.87 MB/s       28156 B/op        236 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic-16                     79009 ns/op        164.98 MB/s       97266 B/op        958 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Fast-16                79669 ns/op        163.61 MB/s       97279 B/op        958 allocs/op
BenchmarkDecoder_Parallel_Generic_StdLib-16                    41571 ns/op        313.56 MB/s       49472 B/op        795 allocs/op
BenchmarkDecoder_Parallel_Generic_JsonIter-16                  68671 ns/op        189.82 MB/s       54299 B/op       1085 allocs/op
BenchmarkDecoder_Parallel_Generic_GoJson-16                    29231 ns/op        445.93 MB/s       64998 B/op        996 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next-16                 2978 ns/op       4377.43 MB/s        7963 B/op         24 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next_Fast-16            1987 ns/op       6559.05 MB/s        4637 B/op         22 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic-16                     35799 ns/op        364.12 MB/s       56440 B/op         91 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Fast-16                33946 ns/op        383.99 MB/s       56584 B/op         93 allocs/op
BenchmarkDecoder_Parallel_Binding_StdLib-16                    35961 ns/op        362.48 MB/s       11419 B/op        160 allocs/op
BenchmarkDecoder_Parallel_Binding_JsonIter-16                  11144 ns/op       1169.72 MB/s       14552 B/op        379 allocs/op
BenchmarkDecoder_Parallel_Binding_GoJson-16                    11926 ns/op       1093.02 MB/s       22184 B/op         49 allocs/op

BenchmarkGetOne_Sonic_Next-16                                   3876 ns/op       3363.36 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Sonic-16                                       22735 ns/op        573.35 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Gjson-16                                        9735 ns/op       1339.00 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Jsoniter-16                                   105439 ns/op        123.63 MB/s       27665 B/op        635 allocs/op
BenchmarkGetOne_Parallel_Sonic_Next-16                         244.5 ns/op      53303.16 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Sonic-16                               1436 ns/op       9075.16 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Gjson-16                              607.9 ns/op      21442.09 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Jsoniter-16                           16468 ns/op        791.54 MB/s       27687 B/op        635 allocs/op
BenchmarkSetOne_Sonic_Next-16                                   6947 ns/op       1876.27 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Sonic-16                                       27328 ns/op        476.98 MB/s        1056 B/op          2 allocs/op
BenchmarkSetOne_Sjson-16                                       50562 ns/op        257.80 MB/s       52146 B/op          9 allocs/op
BenchmarkSetOne_Jsoniter-16                                   158301 ns/op         82.34 MB/s       45122 B/op        952 allocs/op
BenchmarkSetOne_Parallel_Sonic_Next-16                         846.1 ns/op      15405.25 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Parallel_Sonic-16                               1949 ns/op       6689.08 MB/s        1056 B/op          2 allocs/op
BenchmarkSetOne_Parallel_Sjson-16                              36949 ns/op        352.78 MB/s       52289 B/op         10 allocs/op
BenchmarkSetOne_Parallel_Jsoniter-16                           34183 ns/op        381.33 MB/s       45172 B/op        952 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic_Next-16                       5618 ns/op       2320.02 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic-16                           13319 ns/op        978.69 MB/s        3056 B/op         13 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic_Next-16              1311 ns/op       9941.61 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic-16                   2250 ns/op       5793.88 MB/s        3056 B/op         13 allocs/op
BenchmarkLoadNode/Interface()_Sonic_Next-16                    20805 ns/op        626.55 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode/Interface()_Sonic-16                         29595 ns/op        440.45 MB/s       23504 B/op         96 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic_Next-16            7625 ns/op       1709.52 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic-16                14420 ns/op        903.94 MB/s       23506 B/op         96 allocs/op
```

```powershell
goversion: 1.27.0
goos: linux
goarch: riscv64 (GORISCV64=rva23u64)
cpu: RISC-V 64-bit RVA23U64 + RVV 1.0 (VLEN=256)
BenchmarkEncoder_Generic_Sonic_Next-16                         95286 ns/op        136.80 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic_Next_Fast-16                    62444 ns/op        208.75 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Generic_Sonic-16                             158832 ns/op         82.07 MB/s        9577 B/op          8 allocs/op
BenchmarkEncoder_Generic_Sonic_Fast-16                        157937 ns/op         82.53 MB/s        9577 B/op          8 allocs/op
BenchmarkEncoder_Generic_JsonIter-16                          166817 ns/op         78.14 MB/s       17984 B/op        153 allocs/op
BenchmarkEncoder_Generic_GoJson-16                            307892 ns/op         42.34 MB/s       26769 B/op         54 allocs/op
BenchmarkEncoder_Generic_StdLib-16                            165081 ns/op         78.96 MB/s        9576 B/op          8 allocs/op
BenchmarkEncoder_Binding_Sonic_Next-16                         18607 ns/op        700.55 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic_Next_Fast-16                    16382 ns/op        795.70 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_Sonic-16                              88994 ns/op        146.47 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Binding_Sonic_Fast-16                         83231 ns/op        156.61 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Binding_JsonIter-16                           73290 ns/op        177.86 MB/s        9480 B/op          2 allocs/op
BenchmarkEncoder_Binding_GoJson-16                             48866 ns/op        266.75 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Binding_StdLib-16                            100894 ns/op        129.20 MB/s        9504 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next-16                19119 ns/op        681.78 MB/s        9572 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Next_Fast-16           18897 ns/op        689.81 MB/s        9525 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic-16                     26747 ns/op        487.34 MB/s        9654 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Generic_Sonic_Fast-16                24471 ns/op        532.66 MB/s        9650 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Generic_JsonIter-16                  25693 ns/op        507.34 MB/s       17994 B/op        153 allocs/op
BenchmarkEncoder_Parallel_Generic_GoJson-16                    53546 ns/op        243.44 MB/s       26654 B/op         53 allocs/op
BenchmarkEncoder_Parallel_Generic_StdLib-16                    27522 ns/op        473.62 MB/s        9579 B/op          8 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next-16                 8267 ns/op       1576.69 MB/s        9480 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Next_Fast-16            8938 ns/op       1458.32 MB/s        9480 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic-16                     13682 ns/op        952.72 MB/s        9546 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Binding_Sonic_Fast-16                13667 ns/op        953.77 MB/s        9525 B/op          3 allocs/op
BenchmarkEncoder_Parallel_Binding_JsonIter-16                  10452 ns/op       1247.10 MB/s        9480 B/op          2 allocs/op
BenchmarkEncoder_Parallel_Binding_GoJson-16                     7458 ns/op       1747.80 MB/s        9472 B/op          1 allocs/op
BenchmarkEncoder_Parallel_Binding_StdLib-16                    15535 ns/op        839.09 MB/s        9504 B/op          3 allocs/op

BenchmarkDecoder_Generic_Sonic_Next-16                        151786 ns/op         85.88 MB/s       35857 B/op        237 allocs/op
BenchmarkDecoder_Generic_Sonic_Next_Fast-16                   125064 ns/op        104.23 MB/s       27889 B/op        236 allocs/op
BenchmarkDecoder_Generic_Sonic-16                             705719 ns/op         18.47 MB/s       96076 B/op        946 allocs/op
BenchmarkDecoder_Generic_Sonic_Fast-16                        693936 ns/op         18.78 MB/s       96075 B/op        946 allocs/op
BenchmarkDecoder_Generic_StdLib-16                            657192 ns/op         19.83 MB/s       49368 B/op        931 allocs/op
BenchmarkDecoder_Generic_JsonIter-16                          403825 ns/op         32.28 MB/s       54216 B/op       1085 allocs/op
BenchmarkDecoder_Generic_GoJson-16                            390907 ns/op         33.35 MB/s       64866 B/op        996 allocs/op
BenchmarkDecoder_Binding_Sonic_Next-16                         62481 ns/op        208.62 MB/s        7840 B/op         24 allocs/op
BenchmarkDecoder_Binding_Sonic_Next_Fast-16                    50166 ns/op        259.84 MB/s        4480 B/op         22 allocs/op
BenchmarkDecoder_Binding_Sonic-16                             333291 ns/op         39.11 MB/s       55618 B/op         85 allocs/op
BenchmarkDecoder_Binding_Sonic_Fast-16                        334345 ns/op         38.99 MB/s       55618 B/op         85 allocs/op
BenchmarkDecoder_Binding_StdLib-16                            244386 ns/op         53.34 MB/s        8912 B/op         70 allocs/op
BenchmarkDecoder_Binding_JsonIter-16                          183333 ns/op         71.10 MB/s       14536 B/op        379 allocs/op
BenchmarkDecoder_Binding_GoJson-16                            135143 ns/op         96.45 MB/s       21936 B/op         49 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next-16                73950 ns/op        176.27 MB/s       36554 B/op        237 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Next_Fast-16           65245 ns/op        199.79 MB/s       28521 B/op        236 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic-16                    200278 ns/op         65.08 MB/s       97613 B/op        962 allocs/op
BenchmarkDecoder_Parallel_Generic_Sonic_Fast-16               202228 ns/op         64.46 MB/s       97935 B/op        966 allocs/op
BenchmarkDecoder_Parallel_Generic_StdLib-16                    95170 ns/op        136.97 MB/s       49375 B/op        931 allocs/op
BenchmarkDecoder_Parallel_Generic_JsonIter-16                  72743 ns/op        179.19 MB/s       54219 B/op       1085 allocs/op
BenchmarkDecoder_Parallel_Generic_GoJson-16                    76872 ns/op        169.57 MB/s       64879 B/op        996 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next-16                21208 ns/op        614.62 MB/s        8121 B/op         24 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Next_Fast-16           13852 ns/op        941.02 MB/s        4688 B/op         22 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic-16                    108598 ns/op        120.03 MB/s       56749 B/op         94 allocs/op
BenchmarkDecoder_Parallel_Binding_Sonic_Fast-16               111674 ns/op        116.72 MB/s       56715 B/op         94 allocs/op
BenchmarkDecoder_Parallel_Binding_StdLib-16                    38081 ns/op        342.30 MB/s        8913 B/op         70 allocs/op
BenchmarkDecoder_Parallel_Binding_JsonIter-16                  28510 ns/op        457.21 MB/s       14537 B/op        379 allocs/op
BenchmarkDecoder_Parallel_Binding_GoJson-16                    26628 ns/op        489.51 MB/s       21957 B/op         49 allocs/op

BenchmarkGetOne_Sonic_Next-16                                   5515 ns/op       2363.56 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Sonic-16                                       42061 ns/op        309.91 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Gjson-16                                       24472 ns/op        532.65 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Jsoniter-16                                   231224 ns/op         56.37 MB/s       27664 B/op        635 allocs/op
BenchmarkGetOne_Parallel_Sonic_Next-16                         692.6 ns/op      18821.65 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Sonic-16                               2720 ns/op       4792.59 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Gjson-16                               3143 ns/op       4147.34 MB/s           0 B/op          0 allocs/op
BenchmarkGetOne_Parallel_Jsoniter-16                           38062 ns/op        342.47 MB/s       27665 B/op        635 allocs/op
BenchmarkSetOne_Sonic_Next-16                                  14852 ns/op        877.65 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Sonic-16                                       59686 ns/op        218.39 MB/s        1056 B/op          2 allocs/op
BenchmarkSetOne_Sjson-16                                      140964 ns/op         92.47 MB/s       52152 B/op         10 allocs/op
BenchmarkSetOne_Jsoniter-16                                   377623 ns/op         34.52 MB/s       45120 B/op        952 allocs/op
BenchmarkSetOne_Parallel_Sonic_Next-16                          3724 ns/op       3500.64 MB/s        1024 B/op          1 allocs/op
BenchmarkSetOne_Parallel_Sonic-16                               4186 ns/op       3114.28 MB/s        1056 B/op          2 allocs/op
BenchmarkSetOne_Parallel_Sjson-16                              32604 ns/op        399.80 MB/s       52158 B/op         10 allocs/op
BenchmarkSetOne_Parallel_Jsoniter-16                           63985 ns/op        203.72 MB/s       45123 B/op        952 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic_Next-16                      13854 ns/op        940.88 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode/LoadAll()_Sonic-16                           31831 ns/op        409.51 MB/s        3056 B/op         13 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic_Next-16              5754 ns/op       2265.53 MB/s        2072 B/op          4 allocs/op
BenchmarkLoadNode_Parallel/LoadAll()_Sonic-16                   5524 ns/op       2359.87 MB/s        3056 B/op         13 allocs/op
BenchmarkLoadNode/Interface()_Sonic_Next-16                    58613 ns/op        222.39 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode/Interface()_Sonic-16                         91219 ns/op        142.90 MB/s       23504 B/op         96 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic_Next-16           28007 ns/op        465.43 MB/s        9968 B/op         75 allocs/op
BenchmarkLoadNode_Parallel/Interface()_Sonic-16                35767 ns/op        364.45 MB/s       23506 B/op         96 allocs/op
```

</details>

- [Small](https://github.com/bytedance/sonic/blob/main/testdata/small.go) (400B, 11 keys, 3 layers)
![small benchmarks](./docs/imgs/bench-small.png)

<details>
<summary>More architectures — ARM64 / RISC-V RVA22 / RISC-V RVA23 (Small 400B)</summary>

![small benchmarks arm64](./docs/imgs/bench-small-arm64.png)
![small benchmarks rva22](./docs/imgs/bench-small-rva22.png)
![small benchmarks rva23](./docs/imgs/bench-small-rva23.png)

</details>

- [Large](https://github.com/bytedance/sonic/blob/main/testdata/twitter.json) (635KB, 10000+ key, 6 layers)
![large benchmarks](./docs/imgs/bench-large.png)

<details>
<summary>More architectures — ARM64 / RISC-V RVA22 / RISC-V RVA23 (Large 635KB)</summary>

![large benchmarks arm64](./docs/imgs/bench-large-arm64.png)
![large benchmarks rva22](./docs/imgs/bench-large-rva22.png)
![large benchmarks rva23](./docs/imgs/bench-large-rva23.png)

</details>

See [bench.sh](https://github.com/bytedance/sonic/blob/main/scripts/bench.sh) for benchmark codes.

## How it works

See [INTRODUCTION.md](./docs/INTRODUCTION.md).

## Usage

### Marshal/Unmarshal

Default behaviors are mostly consistent with `encoding/json`, except HTML escaping form (see [Escape HTML](https://github.com/bytedance/sonic/blob/main/README.md#escape-html)) and `SortKeys` feature (optional support see [Sort Keys](https://github.com/bytedance/sonic/blob/main/README.md#sort-keys)) that is **NOT** in conformity to [RFC8259](https://datatracker.ietf.org/doc/html/rfc8259).

 ```go
import "github.com/bytedance/sonic"

var data YourSchema
// Marshal
output, err := sonic.Marshal(&data)
// Unmarshal
err := sonic.Unmarshal(output, &data)
 ```

### Streaming IO

Sonic supports decoding json from `io.Reader` or encoding objects into `io.Writer`, aims at handling multiple values as well as reducing memory consumption.

- encoder

```go
var o1 = map[string]interface{}{
    "a": "b",
}
var o2 = 1
var w = bytes.NewBuffer(nil)
var enc = sonic.ConfigDefault.NewEncoder(w)
enc.Encode(o1)
enc.Encode(o2)
fmt.Println(w.String())
// Output:
// {"a":"b"}
// 1
```

- decoder

```go
var o =  map[string]interface{}{}
var r = strings.NewReader(`{"a":"b"}{"1":"2"}`)
var dec = sonic.ConfigDefault.NewDecoder(r)
dec.Decode(&o)
dec.Decode(&o)
fmt.Printf("%+v", o)
// Output:
// map[1:2 a:b]
```

### Use Number/Use Int64

 ```go
import "github.com/bytedance/sonic/decoder"

var input = `1`
var data interface{}

// default float64
dc := decoder.NewDecoder(input)
dc.Decode(&data) // data == float64(1)
// use json.Number
dc = decoder.NewDecoder(input)
dc.UseNumber()
dc.Decode(&data) // data == json.Number("1")
// use int64
dc = decoder.NewDecoder(input)
dc.UseInt64()
dc.Decode(&data) // data == int64(1)

root, err := sonic.GetFromString(input)
// Get json.Number
jn := root.Number()
jm := root.InterfaceUseNumber().(json.Number) // jn == jm
// Get float64
fn := root.Float64()
fm := root.Interface().(float64) // jn == jm
 ```

### Sort Keys

On account of the performance loss from sorting (roughly 10%), sonic doesn't enable this feature by default. If your component depends on it to work (like [zstd](https://github.com/facebook/zstd)), Use it like this:

```go
import "github.com/bytedance/sonic"
import "github.com/bytedance/sonic/encoder"

// Binding map only
m := map[string]interface{}{}
v, err := encoder.Encode(m, encoder.SortMapKeys)

// Or ast.Node.SortKeys() before marshal
var root := sonic.Get(JSON)
err := root.SortKeys()
```

### Escape HTML

On account of the performance loss (roughly 15%), sonic doesn't enable this feature by default. You can use `encoder.EscapeHTML` option to open this feature (align with `encoding/json.HTMLEscape`).

```go
import "github.com/bytedance/sonic"

v := map[string]string{"&&":"<>"}
ret, err := Encode(v, EscapeHTML) // ret == `{"\u0026\u0026":{"X":"\u003c\u003e"}}`
```

### Compact Format

Sonic encodes primitive objects (struct/map...) as compact-format JSON by default, except marshaling `json.RawMessage` or `json.Marshaler`: sonic ensures validating their output JSON but **DO NOT** compacting them for performance concerns. We provide the option `encoder.CompactMarshaler` to add compacting process.

### Print Error

If there invalid syntax in input JSON, sonic will return `decoder.SyntaxError`, which supports pretty-printing of error position

```go
import "github.com/bytedance/sonic"
import "github.com/bytedance/sonic/decoder"

var data interface{}
err := sonic.UnmarshalString("[[[}]]", &data)
if err != nil {
    /* One line by default */
    println(e.Error()) // "Syntax error at index 3: invalid char\n\n\t[[[}]]\n\t...^..\n"
    /* Pretty print */
    if e, ok := err.(decoder.SyntaxError); ok {
        /*Syntax error at index 3: invalid char

            [[[}]]
            ...^..
        */
        print(e.Description())
    } else if me, ok := err.(*decoder.MismatchTypeError); ok {
        // decoder.MismatchTypeError is new to Sonic v1.6.0
        print(me.Description())
    }
}
```

#### Mismatched Types [Sonic v1.6.0]

If there a **mismatch-typed** value for a given key, sonic will report `decoder.MismatchTypeError` (if there are many, report the last one), but still skip wrong the value and keep decoding next JSON.

```go
import "github.com/bytedance/sonic"
import "github.com/bytedance/sonic/decoder"

var data = struct{
    A int
    B int
}{}
err := UnmarshalString(`{"A":"1","B":1}`, &data)
println(err.Error())    // Mismatch type int with value string "at index 5: mismatched type with value\n\n\t{\"A\":\"1\",\"B\":1}\n\t.....^.........\n"
fmt.Printf("%+v", data) // {A:0 B:1}
```

### Ast.Node

Sonic/ast.Node is a completely self-contained AST for JSON. It implements serialization and deserialization both and provides robust APIs for obtaining and modification of generic data.

#### Get/Index

Search partial JSON by given paths, which must be non-negative integer or string, or nil

```go
import "github.com/bytedance/sonic"

input := []byte(`{"key1":[{},{"key2":{"key3":[1,2,3]}}]}`)

// no path, returns entire json
root, err := sonic.Get(input)
raw := root.Raw() // == string(input)

// multiple paths
root, err := sonic.Get(input, "key1", 1, "key2")
sub := root.Get("key3").Index(2).Int64() // == 3
```

**Tip**: since `Index()` uses offset to locate data, which is much faster than scanning like `Get()`, we suggest you use it as much as possible. And sonic also provides another API `IndexOrGet()` to underlying use offset as well as ensure the key is matched.

#### SearchOption

`Searcher` provides some options for user to meet different needs:

```go
opts := ast.SearchOption{ CopyReturn: true ... }
val, err := sonic.GetWithOptions(JSON, opts, "key")
```

- CopyReturn
Indicate the searcher to copy the result JSON string instead of refer from the input. This can help to reduce memory usage if you cache the results
- ConcurentRead
Since `ast.Node` use `Lazy-Load` design, it doesn't support Concurrently-Read by default. If you want to read it concurrently, please specify it.
- ValidateJSON
Indicate the searcher to validate the entire JSON. This option is enabled by default, which slow down the search speed a little.

#### Set/Unset

Modify the json content by Set()/Unset()

```go
import "github.com/bytedance/sonic"

// Set
exist, err := root.Set("key4", NewBool(true)) // exist == false
alias1 := root.Get("key4")
println(alias1.Valid()) // true
alias2 := root.Index(1)
println(alias1 == alias2) // true

// Unset
exist, err := root.UnsetByIndex(1) // exist == true
println(root.Get("key4").Check()) // "value not exist"
```

#### Serialize

To encode `ast.Node` as json, use `MarshalJson()` or `json.Marshal()` (MUST pass the node's pointer)

```go
import (
    "encoding/json"
    "github.com/bytedance/sonic"
)

buf, err := root.MarshalJson()
println(string(buf))                // {"key1":[{},{"key2":{"key3":[1,2,3]}}]}
exp, err := json.Marshal(&root)     // WARN: use pointer
println(string(buf) == string(exp)) // true
```

#### APIs

- validation: `Check()`, `Error()`, `Valid()`, `Exist()`
- searching: `Index()`, `Get()`, `IndexPair()`, `IndexOrGet()`, `GetByPath()`
- go-type casting: `Int64()`, `Float64()`, `String()`, `Number()`, `Bool()`, `Map[UseNumber|UseNode]()`, `Array[UseNumber|UseNode]()`, `Interface[UseNumber|UseNode]()`
- go-type packing: `NewRaw()`, `NewNumber()`, `NewNull()`, `NewBool()`, `NewString()`, `NewObject()`, `NewArray()`
- iteration: `Values()`, `Properties()`, `ForEach()`, `SortKeys()`
- modification: `Set()`, `SetByIndex()`, `Add()`

### Ast.Visitor

Sonic provides an advanced API for fully parsing JSON into non-standard types (neither `struct` not `map[string]interface{}`) without using any intermediate representation (`ast.Node` or `interface{}`). For example, you might have the following types which are like `interface{}` but actually not `interface{}`:

```go
type UserNode interface {}

// the following types implement the UserNode interface.
type (
    UserNull    struct{}
    UserBool    struct{ Value bool }
    UserInt64   struct{ Value int64 }
    UserFloat64 struct{ Value float64 }
    UserString  struct{ Value string }
    UserObject  struct{ Value map[string]UserNode }
    UserArray   struct{ Value []UserNode }
)
```

Sonic provides the following API to return **the preorder traversal of a JSON AST**. The `ast.Visitor` is a SAX style interface which is used in some C++ JSON library. You should implement `ast.Visitor` by yourself and pass it to `ast.Preorder()` method. In your visitor you can make your custom types to represent JSON values. There may be an O(n) space container (such as stack) in your visitor to record the object / array hierarchy.

```go
func Preorder(str string, visitor Visitor, opts *VisitorOptions) error

type Visitor interface {
    OnNull() error
    OnBool(v bool) error
    OnString(v string) error
    OnInt64(v int64, n json.Number) error
    OnFloat64(v float64, n json.Number) error
    OnObjectBegin(capacity int) error
    OnObjectKey(key string) error
    OnObjectEnd() error
    OnArrayBegin(capacity int) error
    OnArrayEnd() error
}
```

See [ast/visitor.go](https://github.com/bytedance/sonic/blob/main/ast/visitor.go) for detailed usage. We also implement a demo visitor for `UserNode` in [ast/visitor_test.go](https://github.com/bytedance/sonic/blob/main/ast/visitor_test.go).

## Compatibility

For developers who want to use sonic to meet different scenarios, we provide some integrated configs as `sonic.API`

- `ConfigDefault`: the sonic's default config (`EscapeHTML=false`,`SortKeys=false`...) to run sonic fast meanwhile ensure security.
- `ConfigStd`: the std-compatible config (`EscapeHTML=true`,`SortKeys=true`...)
- `ConfigFastest`: the fastest config (`NoQuoteTextMarshaler=true`) to run on sonic as fast as possible.
Sonic **DOES NOT** ensure to support all environments, due to the difficulty of developing high-performance codes. On non-sonic-supporting environment, the implementation will fall back to `encoding/json`. Thus below configs will all equal to `ConfigStd`.

## Tips

### Pretouch

Since Sonic uses [golang-asm](https://github.com/twitchyliquid64/golang-asm) as a JIT assembler, which is NOT very suitable for runtime compiling, first-hit running of a huge schema may cause request-timeout or even process-OOM. For better stability, we advise **using `PretouchMany()` for huge-schema or lantency-sensitive applications** before `Marshal()/Unmarshal()`.

```go
import (
    "reflect"
    "github.com/bytedance/sonic"
    "github.com/bytedance/sonic/option"
)

func init() {
    var v1 HugeStruct1
    var v2 HugeStruct2

    // For most large types (nesting depth <= option.DefaultMaxInlineDepth)
    sonic.PretouchMany([]reflect.Type{reflect.TypeOf(v1), reflect.TypeOf(v2)},
        // If the type is too deep nesting (nesting depth > option.DefaultMaxInlineDepth),
        // you can set more recursive loops in Pretouch for fully sufficient JIT.
        option.WithCompileRecursiveDepth(loop),
        // For a large struct, try to set a smaller depth to reduce compiling time.
        option.WithCompileMaxInlineDepth(depth),
    )
}
```

### Copy string

When decoding **string values without any escaped characters**, sonic references them from the origin JSON buffer instead of mallocing a new buffer to copy. This helps a lot for CPU performance but may leave the whole JSON buffer in memory as long as the decoded objects are being used. In practice, we found the extra memory introduced by referring JSON buffer is usually 20% ~ 80% of decoded objects. Once an application holds these objects for a long time (for example, cache the decoded objects for reusing), its in-use memory on the server may go up. - `Config.CopyString`/`decoder.CopyString()`: We provide the option for `Decode()` / `Unmarshal()` users to choose not to reference the JSON buffer, which may cause a decline in CPU performance to some degree.

- `GetFromStringNoCopy()`: For memory safety, `sonic.Get()` / `sonic.GetFromString()` now copies return JSON. If users want to get json more quickly and not care about memory usage, you can use `GetFromStringNoCopy()` to return a JSON directly referenced from source.

### Pass string or []byte?

For alignment to `encoding/json`, we provide API to pass `[]byte` as an argument, but the string-to-bytes copy is conducted at the same time considering safety, which may lose performance when the origin JSON is huge. Therefore, you can use `UnmarshalString()` and `GetFromString()` to pass a string, as long as your origin data is a string or **nocopy-cast** is safe for your []byte. We also provide API `MarshalString()` for convenient **nocopy-cast** of encoded JSON []byte, which is safe since sonic's output bytes is always duplicated and unique.

### Accelerate `encoding.TextMarshaler`

To ensure data security, sonic.Encoder quotes and escapes string values from `encoding.TextMarshaler` interfaces by default, which may degrade performance much if most of your data is in form of them. We provide `encoder.NoQuoteTextMarshaler` to skip these operations, which means you **MUST** ensure their output string escaped and quoted following [RFC8259](https://datatracker.ietf.org/doc/html/rfc8259).

### Better performance for generic data

In **fully-parsed** scenario, `Unmarshal()` performs better than `Get()`+`Node.Interface()`. But if you only have a part of the schema for specific json, you can combine `Get()` and `Unmarshal()` together:

```go
import "github.com/bytedance/sonic"

node, err := sonic.GetFromString(_TwitterJson, "statuses", 3, "user")
var user User // your partial schema...
err = sonic.UnmarshalString(node.Raw(), &user)
```

Even if you don't have any schema, use `ast.Node` as the container of generic values instead of `map` or `interface`:

```go
import "github.com/bytedance/sonic"

root, err := sonic.GetFromString(_TwitterJson)
user := root.GetByPath("statuses", 3, "user")  // === root.Get("status").Index(3).Get("user")
err = user.Check()

// err = user.LoadAll() // only call this when you want to use 'user' concurrently...
go someFunc(user)
```

Why? Because `ast.Node` stores its children using `array`:

- `Array`'s performance is **much better** than `Map` when Inserting (Deserialize) and Scanning (Serialize) data;
- **Hashing** (`map[x]`) is not as efficient as **Indexing** (`array[x]`), which `ast.Node` can conduct on **both array and object**;
- Using `Interface()`/`Map()` means Sonic must parse all the underlying values, while `ast.Node` can parse them **on demand**.

**CAUTION:** `ast.Node` **DOESN'T** ensure concurrent security directly, due to its **lazy-load** design. However, you can call `Node.Load()`/`Node.LoadAll()` to achieve that, which may bring performance reduction while it still works faster than converting to `map` or `interface{}`

### Ast.Node or Ast.Visitor?

For generic data, `ast.Node` should be enough for your needs in most cases.

However, `ast.Node` is designed for partially processing JSON string. It has some special designs such as lazy-load which might not be suitable for directly parsing the whole JSON string like `Unmarshal()`. Although `ast.Node` is better then `map` or `interface{}`, it's also a kind of intermediate representation after all if your final types are customized and you have to convert the above types to your custom types after parsing.

For better performance, in previous case the `ast.Visitor` will be the better choice. It performs JSON decoding like `Unmarshal()` and you can directly use your final types to represents a JSON AST without any intermediate representations.

But `ast.Visitor` is not a very handy API. You might need to write a lot of code to implement your visitor and carefully maintain the tree hierarchy during decoding. Please read the comments in [ast/visitor.go](https://github.com/bytedance/sonic/blob/main/ast/visitor.go) carefully if you decide to use this API.

### Buffer Size

Sonic use memory pool in many places like `encoder.Encode`, `ast.Node.MarshalJSON` to improve performance, which may produce more memory usage (in-use) when server's load is high. See [issue 614](https://github.com/bytedance/sonic/issues/614). Therefore, we introduce some options to let user control the behavior of memory pool. See [option](https://pkg.go.dev/github.com/bytedance/sonic@v1.11.9/option#pkg-variables) package.

### Faster JSON Skip

For security, sonic use [FSM](native/skip_one.c) algorithm to  validate JSON when decoding raw JSON or encoding `json.Marshaler`, which is much slower (1~10x) than [SIMD-searching-pair](native/skip_one_fast.c) algorithm. If user has many redundant JSON value and DO NOT NEED to strictly validate JSON correctness, you can enable below options:

- `Config.NoValidateSkipJSON`: for faster skipping JSON when decoding, such as unknown fields, json.Unmarshaler(json.RawMessage), mismatched values, and redundant array elements
- `Config.NoValidateJSONMarshaler`: avoid validating JSON when encoding `json.Marshaler`
- `SearchOption.ValidateJSON`: indicates if validate located JSON value when `Get`

## JSON-Path Support (GJSON)

[tidwall/gjson](https://github.com/tidwall/gjson) has provided a comprehensive and popular JSON-Path API, and
 a lot of older codes heavily relies on it. Therefore, we provides a wrapper library, which combines gjson's API with sonic's SIMD algorithm to boost up the performance. See [cloudwego/gjson](https://github.com/cloudwego/gjson).

## Community

Sonic is a subproject of [CloudWeGo](https://www.cloudwego.io/). We are committed to building a cloud native ecosystem.
