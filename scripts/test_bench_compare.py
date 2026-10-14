import unittest

from bench_compare import allocations, regressions

OUTPUT = """goos: linux
pkg: axiom/internal/protocol
BenchmarkScan-8          	  120000	      9876 ns/op	     512 B/op	       3 allocs/op
BenchmarkScan-8          	  118000	     10021 ns/op	     512 B/op	       3 allocs/op
BenchmarkScan-8          	  121000	      9790 ns/op	     512 B/op	       4 allocs/op
BenchmarkScan/large-8    	    1000	   1234567 ns/op	   65536 B/op	      12 allocs/op
pkg: axiom/internal/routing
BenchmarkPick            	 5000000	       210 ns/op	       0 B/op	       0 allocs/op
PASS
"""


class Allocations(unittest.TestCase):
    def test_median_per_benchmark_keyed_by_package(self):
        self.assertEqual(allocations(OUTPUT), {
            "axiom/internal/protocol.BenchmarkScan": 3,
            "axiom/internal/protocol.BenchmarkScan/large": 12,
            "axiom/internal/routing.BenchmarkPick": 0,
        })


class Regressions(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("equal", {"a": 3}, {"a": 3}, []),
            ("fewer", {"a": 3}, {"a": 2}, []),
            ("more", {"a": 3}, {"a": 4}, ["a: 3 -> 4 allocs/op"]),
            ("new and removed benchmarks are not compared", {"old": 1}, {"new": 9}, []),
        ]
        for name, base, head, expected in cases:
            with self.subTest(name):
                self.assertEqual(regressions(base, head), expected)


if __name__ == "__main__":
    unittest.main()
