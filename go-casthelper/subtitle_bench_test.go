package main

import (
	"context"
	"os"
	"testing"
)

// Opt in with IINA_BENCH_MEDIA=/absolute/movie.mkv go test -run '^$'
// -bench BenchmarkColdEmbeddedSubtitles -benchtime=1x. Each case uses a fresh
// output cache; operating-system filesystem caches are deliberately left alone.
func BenchmarkColdEmbeddedSubtitles(b *testing.B) {
	source := os.Getenv("IINA_BENCH_MEDIA")
	if source == "" {
		b.Skip("set IINA_BENCH_MEDIA to a local test movie")
	}
	info, err := probeMedia(context.Background(), source)
	if err != nil {
		b.Fatal(err)
	}
	_, assets := subtitleCatalog(source, nil, info)
	var specs []subtitleSpec
	for _, asset := range assets {
		specs = append(specs, asset.spec)
	}
	if len(specs) < 2 {
		b.Skip("movie needs multiple text tracks")
	}
	for _, batch := range []bool{false, true} {
		name := "one_process_per_track"
		if batch {
			name = "one_batch"
		}
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				p := newSubtitlePreparer(b.TempDir())
				b.StartTimer()
				if batch {
					if _, err := p.prepare(context.Background(), source, specs); err != nil {
						b.Fatal(err)
					}
				} else {
					for _, spec := range specs {
						if _, err := p.prepare(context.Background(), source, []subtitleSpec{spec}); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
