package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type preparedSubtitle struct{ file, token string }
type subtitleTask struct {
	spec                           subtitleSpec
	source, mapping, target, token string
}
type subtitleBatch struct {
	cancel   context.CancelFunc
	refs     int
	finished bool
}
type subtitleJob struct {
	task  subtitleTask
	batch *subtitleBatch
	done  chan struct{}
	err   error
}

// One preparation interface is shared by casting, previews and receiver fetches.
// Jobs are keyed by output identity, so a cancelled preview cannot terminate
// extraction still needed by a cast or another request.
type subtitlePreparer struct {
	mu      sync.Mutex
	cache   string
	jobs    map[string]*subtitleJob
	extract func(context.Context, []subtitleTask) []error
}

func newSubtitlePreparer(cache string) *subtitlePreparer {
	return &subtitlePreparer{cache: cache, jobs: map[string]*subtitleJob{}, extract: extractSubtitleBatch}
}
func (p *subtitlePreparer) task(media string, spec subtitleSpec) (subtitleTask, error) {
	source, mapping := media, fmt.Sprintf("0:%d", spec.StreamIndex)
	if spec.ExternalPath != "" {
		source, mapping = spec.ExternalPath, "0:s:0"
	} else if spec.StreamIndex < 0 {
		return subtitleTask{}, fmt.Errorf("no subtitle stream selected")
	}
	if !filepath.IsAbs(source) {
		return subtitleTask{}, fmt.Errorf("subtitle source must be a local absolute path")
	}
	st, err := os.Stat(source)
	if err != nil {
		return subtitleTask{}, err
	}
	if !st.Mode().IsRegular() {
		return subtitleTask{}, fmt.Errorf("subtitle source is not a regular file")
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("layout-v1|%s|%d|%d|%s|%s|%g", source, st.Size(), st.ModTime().UnixNano(), mapping, spec.Codec, spec.Delay)))
	token := fmt.Sprintf("%x", key[:16])
	return subtitleTask{spec, source, mapping, filepath.Join(p.cache, token+".vtt"), token}, nil
}
func validSubtitle(path string) bool {
	body, err := os.ReadFile(path)
	return err == nil && isWebVTT(body)
}
func (p *subtitlePreparer) prepare(ctx context.Context, media string, specs []subtitleSpec) ([]preparedSubtitle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.cache, 0700); err != nil {
		return nil, err
	}
	tasks := make([]subtitleTask, len(specs))
	for i, spec := range specs {
		task, err := p.task(media, spec)
		if err != nil {
			return nil, err
		}
		tasks[i] = task
	}
	p.mu.Lock()
	waiting := make([]*subtitleJob, 0, len(tasks))
	groups := map[string][]*subtitleJob{}
	for _, task := range tasks {
		if validSubtitle(task.target) {
			continue
		}
		job := p.jobs[task.target]
		if job == nil {
			job = &subtitleJob{task: task, done: make(chan struct{})}
			p.jobs[task.target] = job
			groups[task.source] = append(groups[task.source], job)
		}
		waiting = append(waiting, job)
	}
	for _, jobs := range groups {
		runCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		batch := &subtitleBatch{cancel: cancel}
		for _, job := range jobs {
			job.batch = batch
		}
		go p.run(runCtx, jobs)
	}
	for _, job := range waiting {
		job.batch.refs++
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, job := range waiting {
			job.batch.refs--
			if job.batch.refs == 0 && !job.batch.finished {
				for key, pending := range p.jobs {
					if pending.batch == job.batch {
						delete(p.jobs, key)
					}
				}
				job.batch.cancel()
			}
		}
	}()
	for _, job := range waiting {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-job.done:
			if job.err != nil {
				return nil, job.err
			}
		}
	}
	result := make([]preparedSubtitle, len(tasks))
	for i, task := range tasks {
		result[i] = preparedSubtitle{task.target, task.token}
	}
	return result, nil
}
func (p *subtitlePreparer) run(ctx context.Context, jobs []*subtitleJob) {
	tasks := make([]subtitleTask, len(jobs))
	for i, job := range jobs {
		tasks[i] = job.task
	}
	errs := p.extract(ctx, tasks)
	p.mu.Lock()
	defer p.mu.Unlock()
	batch := jobs[0].batch
	batch.finished = true
	batch.cancel()
	for i, job := range jobs {
		if len(errs) != len(jobs) {
			job.err = fmt.Errorf("subtitle preparation returned incomplete results")
		} else {
			job.err = errs[i]
		}
		if p.jobs[job.task.target] == job {
			delete(p.jobs, job.task.target)
		}
		close(job.done)
	}
}
func extractSubtitleBatch(ctx context.Context, tasks []subtitleTask) []error {
	errs := make([]error, len(tasks))
	temps := make([]string, len(tasks))
	var original []byte
	if tasks[0].spec.ExternalPath != "" {
		original, _ = os.ReadFile(tasks[0].source)
	}
	args := []string{"-nostdin", "-v", "error", "-y", "-copyts", "-i", tasks[0].source}
	for i, task := range tasks {
		file, err := os.CreateTemp(filepath.Dir(task.target), "extract-*.vtt")
		if err != nil {
			errs[i] = err
			continue
		}
		file.Close()
		temps[i] = file.Name()
		defer os.Remove(file.Name())
		codec := "webvtt"
		if task.spec.Codec == "webvtt" {
			codec = "copy"
		}
		args = append(args, "-map", task.mapping, "-vn", "-an", "-c:s", codec, "-f", "webvtt", file.Name())
	}
	var extractionErr error
	if !isWebVTT(original) {
		outputs := 0
		for _, temp := range temps {
			if temp != "" {
				outputs++
			}
		}
		if outputs > 0 {
			out, err := exec.CommandContext(ctx, mediaTool("ffmpeg"), args...).CombinedOutput()
			if err != nil {
				extractionErr = fmt.Errorf("subtitle extraction failed: %w: %s", err, truncate(strings.TrimSpace(string(out)), 600))
			}
			logInfo("subtitles: extracted %d tracks in one pass", outputs)
		}
	}
	for i, task := range tasks {
		if errs[i] != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			errs[i] = err
			continue
		}
		body := original
		err := extractionErr
		if err == nil && !isWebVTT(body) {
			body, err = os.ReadFile(temps[i])
		}
		if err == nil && !isWebVTT(body) {
			if extractionErr != nil {
				err = extractionErr
			} else {
				err = fmt.Errorf("extraction did not produce WebVTT")
			}
		}
		if err == nil {
			body, err = shiftWebVTT(body, task.spec.Delay)
		}
		if err == nil {
			err = os.WriteFile(temps[i], body, 0600)
		}
		if err == nil {
			err = os.Rename(temps[i], task.target)
		}
		errs[i] = err
	}
	return errs
}
