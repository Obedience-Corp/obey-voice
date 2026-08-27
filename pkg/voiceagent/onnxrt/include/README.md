# Vendored ONNX Runtime C API headers

`onnxruntime_c_api.h` and `onnxruntime_ep_c_api.h` are copied verbatim from
[microsoft/onnxruntime](https://github.com/microsoft/onnxruntime) at tag
**v1.27.0**, MIT licensed, Copyright (c) Microsoft Corporation.

They are vendored rather than fetched because the version must match the
`libonnxruntime` that obey-voice already links through `sherpa-onnx-go`
(**1.27.0**, `ORT_API_VERSION 27`). `OrtGetApiBase()->GetApi()` returns NULL when
asked for a newer API version than the shared library implements, so a mismatch
fails at runtime rather than at build time.

**When bumping `sherpa-onnx-go`, re-copy these headers from the matching ONNX
Runtime tag.**
