# Pipeline trace contract fixtures

Adopted from completed campaign design WI-b04133 at campaign revision cba388a4.
Source baseline: obey-voice 239c3cf8. Contract version: obey-voice.trace/v1; additive to transcript header v2.

These are synthetic evidence, not measured voice runs. Null means an unavailable measurement; zero means a measured boundary at the origin. Overlapping stages must not be summed. Fallback retains distinct attempts; interrupted operations keep their terminal status. Incomplete/partial fixtures retain missing boundaries and sequence gaps. Unknown fields/types remain inspectable. Legacy captures without trace_schema have reduced correlation and timing capability.

SHA256 of original design fixtures:

```
567241aab9cacb8170482fbfd36675de3a68a7576e18c419e02868be63f64285  fallback.jsonl
eb56cb3e3a99552c8a90888cec6289c02889b1fffc3536c4e700ed94fbdcaf16  incomplete.jsonl
6fe9e2210b4db15444e28a41f969c5744f22a9b3549026be61aa3dde60f89a14  late-attach.jsonl
d2e5a0d30f025e77bbc636ce0a47365f01ef71158e25e83f70da1d579f1b57f8  overlap-interrupted.jsonl
33419aa937e23269e68563c7d50a5fe838537eed05610dfd0698ae4712e73fcc  partial.jsonl
36b14db3ca5ad90fdf9e572901fa680c412a34c8f6a844cfbfa2e943031e5e36  unknown.jsonl
```

Implementation clarification: externally scoped legacy work can carry its own runtime identity without the source monotonic origin. Its additive trace retains that scope and uses null mono_ms, rather than inventing zero or borrowing the current runtime clock. New pipeline-owned traces always carry finite source offsets. The observer must retain raw inspection and label this timing unavailable.
