# Retained ARM64 residual-hotspot results

Median microseconds; changes are medians of same-round ns/op ratios. Negative is faster. Decoder uses Runs; AST is shared. C is original a7193312; before is the preceding retained source. New guard fixtures compare before/after only. No weighted aggregate is inferred.

## core

| Benchmark | C (us) | Before (us) | Retained (us) | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| BenchmarkARMASTMaterialize | 102.5620 | 106.4095 | 106.4405 | +0.15% | +3.80% |
| BenchmarkARMPublicAPI/Default/Marshal/Binding | 12.8415 | 12.8360 | 12.8310 | -0.04% | -0.01% |
| BenchmarkARMPublicAPI/Default/Marshal/Generic | 39.0170 | 35.0870 | 34.9940 | -0.32% | -10.23% |
| BenchmarkARMPublicAPI/Default/Unmarshal/Binding | 23.7545 | 23.5295 | 23.6960 | +0.43% | -0.19% |
| BenchmarkARMPublicAPI/Default/Unmarshal/Generic | 32.5810 | 32.3820 | 32.5500 | +0.41% | -0.15% |
| BenchmarkARMPublicAPI/Std/Marshal/Binding | 18.1140 | 14.5685 | 14.5780 | +0.07% | -19.65% |
| BenchmarkARMPublicAPI/Std/Marshal/Generic | 56.9690 | 53.9860 | 53.9540 | +0.01% | -5.20% |
| BenchmarkARMPublicAPI/Std/Unmarshal/Binding | 32.8135 | 28.2990 | 28.3695 | +0.09% | -13.62% |
| BenchmarkARMPublicAPI/Std/Unmarshal/Generic | 56.7325 | 52.0095 | 52.0795 | +0.18% | -8.12% |

## shapes

| Benchmark | C (us) | Before (us) | Retained (us) | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| BenchmarkARMPublicShapeCoverage/DenseASCIIEscapes/Marshal | 13.5745 | 5.9770 | 5.9910 | +0.16% | -55.83% |
| BenchmarkARMPublicShapeCoverage/DenseASCIIEscapes/Unmarshal | 6.8730 | 5.6335 | 5.6470 | +0.14% | -17.84% |
| BenchmarkARMPublicShapeCoverage/DenseUnicodeEscapes/Marshal | 0.7242 | 0.6034 | 0.6047 | +0.62% | -16.54% |
| BenchmarkARMPublicShapeCoverage/DenseUnicodeEscapes/Unmarshal | 3.9450 | 3.4690 | 3.4745 | +0.17% | -11.94% |
| BenchmarkARMPublicShapeCoverage/LongPlainString/Marshal | 1.5870 | 1.2905 | 1.3010 | +0.54% | -18.41% |
| BenchmarkARMPublicShapeCoverage/LongPlainString/Unmarshal | 1.7085 | 1.6930 | 1.6905 | -0.06% | -0.94% |
| BenchmarkARMPublicShapeCoverage/MixedFloats/Marshal | 18.4815 | 22.3410 | 22.1795 | -0.66% | +20.25% |
| BenchmarkARMPublicShapeCoverage/MixedFloats/Unmarshal | 12.4020 | 14.4565 | 14.4765 | +0.15% | +16.77% |
| BenchmarkARMPublicShapeCoverage/NestedContainers/Marshal | 54.7510 | 51.5920 | 51.2725 | -0.73% | -6.65% |
| BenchmarkARMPublicShapeCoverage/NestedContainers/Unmarshal | 49.7425 | 50.6270 | 50.5980 | -0.09% | +1.81% |
| BenchmarkARMPublicShapeCoverage/SmallIntegers/Marshal | 12.5935 | 9.7435 | 9.8105 | +0.98% | -22.79% |
| BenchmarkARMPublicShapeCoverage/SmallIntegers/Unmarshal | 6.2525 | 5.9790 | 5.9880 | +0.17% | -4.36% |

## coverage

| Benchmark | C (us) | Before (us) | Retained (us) | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| BenchmarkARMNativeCoverage/Numbers/LongDecimals | 227.2970 | 67.6520 | 68.0265 | +0.46% | -70.11% |
| BenchmarkARMNativeCoverage/Numbers/Mixed | 24.8635 | 27.0070 | 27.0450 | +0.17% | +8.73% |
| BenchmarkARMNativeCoverage/Numbers/ShortIntegers | 11.7595 | 11.4025 | 11.3945 | +0.14% | -2.98% |
| BenchmarkARMNativeCoverage/Whitespace/Compact | 28.1185 | 26.6945 | 26.6740 | -0.02% | -5.16% |
| BenchmarkARMNativeCoverage/Whitespace/Pretty | 36.1330 | 32.3030 | 32.0255 | -0.94% | -11.37% |
| BenchmarkARMNativeCoverage/Whitespace/WideIndent | 60.5520 | 40.7470 | 40.7200 | +0.00% | -32.59% |

## ast

| Benchmark | C (us) | Before (us) | Retained (us) | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| BenchmarkGetFullFlowCoverage/EscapedValues | 10.9795 | 7.8250 | 7.7795 | -0.63% | -29.14% |
| BenchmarkGetFullFlowCoverage/FloatArray | 18.9865 | 22.6225 | 18.9910 | -16.08% | +0.13% |
| BenchmarkGetFullFlowCoverage/LongEscapedString | 2.1905 | 1.5370 | 1.5355 | -0.10% | -29.89% |
| BenchmarkGetFullFlowCoverage/LongIntegerArray | 38.7620 | 22.7165 | 22.7355 | -0.59% | -41.29% |
| BenchmarkGetFullFlowCoverage/LongPlainString | 5.4985 | 2.1045 | 2.1040 | -0.09% | -61.74% |
| BenchmarkGetFullFlowCoverage/ManyFields | 94.0140 | 47.3915 | 47.5785 | +0.34% | -49.35% |
| BenchmarkGetFullFlowCoverage/MixedStringLengths | 9.4945 | 6.9700 | 6.9570 | -0.04% | -26.73% |
| BenchmarkGetFullFlowCoverage/NestedContainers | 1.6125 | 1.0530 | 1.0520 | -0.05% | -34.62% |
| BenchmarkGetFullFlowCoverage/NumberArray | 15.2600 | 10.3730 | 10.4085 | +0.39% | -31.76% |
| BenchmarkGetFullFlowCoverage/TinyObjects | 15.7555 | 7.4405 | 7.4085 | -0.56% | -53.02% |
| BenchmarkGetFullFlowCoverage/TwitterCompact | 7.7755 | 5.0270 | 5.0125 | -0.29% | -35.41% |
| BenchmarkGetFullFlowCoverage/TwitterPretty | 10.6870 | 8.7900 | 8.7395 | -0.47% | -18.20% |
| BenchmarkGetFullFlowCoverage/WideWhitespace | 14.9520 | 6.1030 | 6.0475 | -0.85% | -59.55% |
| BenchmarkGetFull_Sonic | 10.7330 | 8.9130 | 8.8835 | -0.39% | -17.25% |
| BenchmarkGetManyShortEscapedFields | 0.3750 | 0.4201 | 0.4184 | -0.25% | +11.66% |
| BenchmarkGetOne_Sonic | 3.3395 | 3.6425 | 3.6510 | +0.19% | +9.29% |
| BenchmarkGetScanArrayIndex/denseStrings/compact/last | 1.4870 | 1.9195 | 1.8840 | -1.77% | +26.66% |
| BenchmarkGetScanArrayIndex/denseStrings/compact/missing | 1.4345 | 1.8690 | 1.8365 | -1.77% | +28.06% |
| BenchmarkGetScanArrayIndex/denseStrings/spaced/last | 1.6100 | 2.2140 | 2.1540 | -2.71% | +33.73% |
| BenchmarkGetScanArrayIndex/denseStrings/spaced/missing | 1.5570 | 2.1570 | 2.0950 | -2.81% | +34.53% |
| BenchmarkGetScanArrayIndex/emptyContainers/compact/last | 1.7180 | 0.5833 | 0.5554 | -4.79% | -67.65% |
| BenchmarkGetScanArrayIndex/emptyContainers/compact/missing | 1.6780 | 0.5351 | 0.5078 | -5.05% | -69.74% |
| BenchmarkGetScanArrayIndex/emptyContainers/spaced/last | 1.8095 | 0.9804 | 0.9599 | -2.07% | -46.93% |
| BenchmarkGetScanArrayIndex/emptyContainers/spaced/missing | 1.7795 | 0.9225 | 0.9052 | -1.83% | -49.15% |
| BenchmarkGetScanArrayIndex/mixed/compact/last | 1.0485 | 0.9849 | 0.9752 | -0.96% | -6.96% |
| BenchmarkGetScanArrayIndex/mixed/compact/missing | 1.0065 | 0.9327 | 0.9210 | -1.22% | -8.45% |
| BenchmarkGetScanArrayIndex/mixed/spaced/last | 1.1490 | 1.3410 | 1.3320 | -0.67% | +15.93% |
| BenchmarkGetScanArrayIndex/mixed/spaced/missing | 1.1060 | 1.2760 | 1.2655 | -0.82% | +14.47% |
| BenchmarkGetWithManyCompare_Sonic | 2.4990 | 1.3005 | 1.3025 | +0.19% | -47.85% |
| BenchmarkParseSeven_Sonic | 14.1450 | 15.2700 | 15.2715 | +0.10% | +8.05% |
| BenchmarkParser_Sonic | 4.3705 | 4.6085 | 4.5695 | -0.84% | +4.50% |
| BenchmarkPathCacheLongQueries/matchKey4096 | 0.8610 | 0.4290 | 0.4291 | +0.12% | -50.16% |
| BenchmarkPathCacheLongQueries/missingAfterLongKey4096 | 0.5377 | 0.2479 | 0.2481 | +0.08% | -53.86% |
| BenchmarkPathCacheLongQueries/skipKey128 | 0.1264 | 0.1465 | 0.1437 | -0.89% | +13.59% |
| BenchmarkPathCacheLongQueries/skipKey4096 | 0.6155 | 0.3257 | 0.3232 | -0.86% | -47.43% |
| BenchmarkPathCacheLongQueries/skipNumber4096 | 0.5282 | 0.6242 | 0.4592 | -26.44% | -13.02% |
| BenchmarkPathCacheLongQueries/skipString4096 | 0.6262 | 0.3270 | 0.3272 | +0.21% | -47.75% |
| BenchmarkPathCacheLongQueries/skipStringDense4096 | 0.7454 | 0.5756 | 0.5738 | -0.25% | -23.00% |
| BenchmarkPathCacheLongQueries/skipStringSparse4096 | 0.6471 | 0.4256 | 0.4229 | -0.47% | -34.64% |

## guards

| Benchmark | Before (us) | Retained (us) | vs before |
| --- | ---: | ---: | ---: |
| BenchmarkARMPublicNumericTransitions/Alternating | 8.0215 | 8.0320 | +0.24% |
| BenchmarkARMPublicNumericTransitions/Alternating19Digits | 9.2210 | 9.2385 | +0.24% |
| BenchmarkARMPublicNumericTransitions/Alternating8Digits | 8.4010 | 8.4020 | +0.07% |
| BenchmarkARMPublicNumericTransitions/FloatThenIntegers | 6.0080 | 6.0410 | +0.24% |
| BenchmarkARMPublicNumericTransitions/ObjectFloats | 18.5710 | 18.6140 | +0.41% |
| BenchmarkARMPublicNumericTransitions/WhitespaceFloats | 12.4495 | 12.4910 | +0.16% |
