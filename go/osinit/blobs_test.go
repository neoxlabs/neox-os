package osinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 一张最小的 png(1×1) —— 只要文件头对, 认格式就够了
var onePNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("rest of it")...)

func TestBlob按内容命名同一张只存一份(t *testing.T) {
	store := blobStore{dir: t.TempDir()}
	a, err := store.save("截图.png", onePNG)
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.save("换了个名字.png", onePNG)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("同样的内容存成了两份: %s / %s", a.ID, b.ID)
	}
	files, _ := os.ReadDir(store.dir)
	if len(files) != 1 {
		t.Fatalf("盘上有 %d 个文件", len(files))
	}
	if a.Mime != "image/png" || !strings.HasSuffix(a.Path, ".png") {
		t.Fatalf("认错了: %+v", a)
	}
}

// **收不下就明说**: 悄悄存一个界面渲染不出、模型也看不了的文件,
// 比拒绝糟得多 —— 用户会以为图发出去了
func TestBlob不是图就不收(t *testing.T) {
	store := blobStore{dir: t.TempDir()}
	if _, err := store.save("其实是文本.png", []byte("这不是图")); err == nil {
		t.Fatal("什么都往里存")
	}
}

func TestBlob没配存图的地方就说没配(t *testing.T) {
	if _, err := (blobStore{}).save("a.png", onePNG); err == nil {
		t.Fatal("没有目录也说存成了")
	}
}

/**
 * 给 bot 的那段话: **给路径, 不是给图**.
 *
 *	主模型认不认图是另一回事(engine/vision.go: 只认显式声明, 不按
 *	模型名猜). 给路径这条路对任何模型都是通的.
 */
func TestImageNote看得了和看不了说的不是一句话(t *testing.T) {
	refs := []blobRef{{ID: "abc.png", Name: "报错.png", Path: "/tmp/blobs/abc.png"}}

	can := imageNote(refs, true)
	if !strings.Contains(can, "/tmp/blobs/abc.png") {
		t.Error("没告诉它图在哪儿")
	}
	if !strings.Contains(can, "view_image") {
		t.Error("没告诉它怎么看")
	}

	cant := imageNote(refs, false)
	if strings.Contains(cant, "view_image") {
		t.Error("这台机器看不了图, 却让它去调 view_image")
	}
	// 看不了就说看不了 —— 最坏的结果是它一本正经地描述一张没收到的图
	if !strings.Contains(cant, "看不了") {
		t.Errorf("没说清它看不了:\n%s", cant)
	}
	if imageNote(nil, true) != "" {
		t.Error("一张图都没有还写了一段")
	}
}

func TestSniff按内容认格式(t *testing.T) {
	cases := []struct {
		data []byte
		want string
	}{
		{onePNG, "image/png"},
		{[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, "image/jpeg"},
		{append([]byte("RIFF1234WEBPVP8 "), 0), "image/webp"},
		{[]byte("GIF89a....."), "image/gif"},
		{[]byte("#!/bin/sh\n"), ""},
	}
	for _, one := range cases {
		got, _, ok := sniff(one.data)
		if one.want == "" && ok {
			t.Errorf("把 %q 当成了图", one.data[:6])
		}
		if one.want != "" && got != one.want {
			t.Errorf("认成了 %q, 该是 %q", got, one.want)
		}
	}
}

// ../ 不许翻出去 —— id 只取文件名那一段
func TestBlobID挡掉路径穿越(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.png"), onePNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base("../../etc/passwd"); got != "passwd" {
		t.Fatalf("Base 的行为变了: %q", got)
	}
}

// 附件: 什么类型都收, 但扩展名是别人起的名字 —— 认不出就 .bin
func TestSaveFile收附件且扩展名收紧(t *testing.T) {
	b := blobStore{dir: t.TempDir()}
	cases := []struct {
		name    string
		wantExt string
	}{
		{"报表.csv", ".csv"},
		{"数据.zip", ".zip"},
		{"没扩展名", ".bin"},
		{"坏名字.sh;rm", ".bin"},         // 扩展名里藏了别的东西
		{"超长.abcdefghijklmn", ".bin"}, // 长得不像扩展名
	}
	for _, one := range cases {
		ref, err := b.saveFile(one.name, []byte("内容"))
		if err != nil {
			t.Fatalf("%s 存不下: %v", one.name, err)
		}
		if filepath.Ext(ref.Path) != one.wantExt {
			t.Errorf("%s 存成了 %q, 扩展名该是 %q", one.name, ref.Path, one.wantExt)
		}
		// 不带执行位 —— 附件是给 bot 读的, 不是给谁跑的
		info, _ := os.Stat(ref.Path)
		if info.Mode().Perm()&0o111 != 0 {
			t.Errorf("%s 带了执行位: %v", one.name, info.Mode())
		}
	}
	// 同样内容只占一份 —— 按内容寻址
	a, _ := b.saveFile("一样.txt", []byte("同一份"))
	c, _ := b.saveFile("一样.txt", []byte("同一份"))
	if a.Path != c.Path {
		t.Errorf("同样内容存了两份: %q / %q", a.Path, c.Path)
	}
}

// 附件不进 /blob 读出口 —— 只放媒体过的规矩不因为附件破掉
func TestSaveFile不进读出口(t *testing.T) {
	b := blobStore{dir: t.TempDir()}
	ref, err := b.saveFile("秘密.txt", []byte("纯文本, sniff 认不出"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := sniff([]byte("纯文本, sniff 认不出")); ok {
		t.Fatal("sniff 把纯文本当成了媒体 —— 读出口的闸失效了")
	}
	_ = ref
}
