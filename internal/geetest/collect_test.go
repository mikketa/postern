package geetest

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Collects icon challenges to a directory, to fit the recogniser against.
//
// One page load and then the widget's own refresh button, rather than one
// visit per sample: this is somebody else's demo and a sample should not cost
// a full page load. Expect duplicates — refreshing reserves challenges — so
// de-duplicate by image before using them, or the same picture lands on both
// sides of a train/test split.
//
//	DISPLAY=:99 COLLECT_DIR=<dir> COLLECT_N=40 COLLECT_FROM=1 \
//	    go test ./internal/geetest -run TestCollectIcons -v
func TestCollectIcons(t *testing.T) {
	dir := os.Getenv("COLLECT_DIR")
	if dir == "" || os.Getenv("DISPLAY") == "" {
		t.Skip("no COLLECT_DIR or DISPLAY")
	}
	want := 12
	if v := os.Getenv("COLLECT_N"); v != "" {
		want, _ = strconv.Atoi(v)
	}
	from := 1
	if v := os.Getenv("COLLECT_FROM"); v != "" {
		from, _ = strconv.Atoi(v)
	}

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("headless", false),
		chromedp.Flag("ozone-platform", "x11"), chromedp.Env("WAYLAND_DISPLAY="))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, time.Duration(120+want*8)*time.Second)
	defer cancelT()

	if err := chromedp.Run(ctx, chromedp.Navigate(demo), chromedp.Sleep(14*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := clickLabel(ctx, "Icon CAPTCHA"); err != nil {
		t.Fatal(err)
	}
	chromedp.Run(ctx, chromedp.Sleep(5*time.Second))
	clickSelector(ctx, "[class*=geetest_btn_click]")
	chromedp.Run(ctx, chromedp.Sleep(8*time.Second))

	written := 0
	for i := from; i < from+want; i++ {
		var w struct {
			Bg     *Box `json:"bg"`
			Prompt *Box `json:"prompt"`
		}
		if err := evaluateInto(ctx, readIcon, &w); err != nil || w.Bg == nil || w.Prompt == nil {
			refreshOnce(ctx)
			continue
		}
		sub := fmt.Sprintf("%s/%02d", dir, i)
		os.MkdirAll(sub, 0o755)
		ok := true
		for name, box := range map[string]Box{"bg": *w.Bg, "ques": *w.Prompt} {
			raw, err := captureRaw(ctx, box)
			if err != nil {
				ok = false
				continue
			}
			os.WriteFile(sub+"/"+name+".png", raw, 0o644)
		}
		if ok {
			written++
		}
		refreshOnce(ctx)
	}
	t.Logf("collected %d", written)
}

func refreshOnce(ctx context.Context) {
	clickIfPresent(ctx, "[class*=geetest_refresh]")
	chromedp.Run(ctx, chromedp.Sleep(3200*time.Millisecond))
}
