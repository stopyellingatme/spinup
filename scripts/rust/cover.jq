# cover.jq: the line coverage of each crate in $names from a cargo llvm-cov JSON summary
# (make rs-cover). Prints "<crate> <percent> ok|FAIL" with a $floor percent floor.
($names | split(" ")) as $n
| [.data[0].files[] | {c: ((.filename | (try capture("/crates/(?<c>[^/]+)/src/").c catch "")) // ""), l: .summary.lines}
   | select(.c as $x | $n | index($x))]
| group_by(.c)[] | (map(.l.covered) | add) as $cov | (map(.l.count) | add) as $all
| "\(.[0].c) \($cov * 1000 / $all | floor / 10) \(if $cov * 100 < $floor * $all then "FAIL" else "ok" end)"
