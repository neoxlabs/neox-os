package osinit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

/**
 * blobs —— **用户发进来的图, 存成文件**.
 *
 * ── 为什么不塞进事件里 ──
 *
 *	一张手机截图两三兆. 事件日志是要整段重放的(界面一连上就补齐历史),
 *	把图片按 base64 塞进去, 每次刷新都要把这几兆重新推一遍, 而且它会
 *	永远待在账本里 —— 账本本来是一行行的文字, 几百 KB 就能装下一整天.
 *
 *	所以图片落成文件, 事件里只留一个 id. 界面按 id 来取, 浏览器自己
 *	会缓存.
 *
 * ── 为什么顺手也是 bot 看图的入口 ──
 *
 *	bot 看图走的是 view_image(路径), 而它要的正是"这张图在磁盘上的哪儿".
 *	同一份文件, 两边各取所需: 界面按 id 取来渲染, bot 按路径打开.
 *	另存一份给模型是白存, 而且两份迟早不一致.
 *
 * ── 按内容命名 ──
 *
 *	文件名是内容的 sha256. 同一张图发两次只占一份, 而且**改不了内容
 *	却保持同一个 id** —— 界面缓存起来才安全.
 */

type blobStore struct{ dir string }

// blobRef 一张存下来的图 —— 界面要 id, bot 要 path.
type blobRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
	Path string `json:"-"`
}

/**
 * 认得的那几种. **按内容判, 不按扩展名判** —— 扩展名是别人起的名字.
 *
 *	这个口子**只放媒体过**: 图、声音、视频. 不是"任何文件都能取" ——
 *	一个能吐任意文件的 HTTP 口, 迟早会被当成别的东西用. 认不出来的
 *	一律 404, 哪怕它就躺在那儿.
 *
 *	用户发进来的仍然只收图(save 那一侧另外把关): 这里认得声音和视频,
 *	是因为 **bot 也能拿一张卡出来** —— 它录了一段、下载了一段, 卡片要
 *	播得出来.
 */
var blobKinds = []struct {
	mime, ext string
	head      func([]byte) bool
}{
	{"image/png", ".png", func(b []byte) bool { return len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" }},
	{"image/jpeg", ".jpg", func(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }},
	{"image/webp", ".webp", func(b []byte) bool {
		return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	}},
	{"image/gif", ".gif", func(b []byte) bool {
		return len(b) > 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a")
	}},
	{"image/svg+xml", ".svg", func(b []byte) bool {
		// svg 是文本, 没有魔数 —— 看开头这一小段里有没有 <svg
		head := strings.ToLower(string(b[:min(len(b), 256)]))
		return strings.Contains(head, "<svg")
	}},
	// ── 声音和视频: bot 拿一张卡出来时要播得出来 ──
	{"audio/mpeg", ".mp3", func(b []byte) bool {
		return len(b) > 3 && (string(b[:3]) == "ID3" || (b[0] == 0xFF && b[1]&0xE0 == 0xE0))
	}},
	{"audio/wav", ".wav", func(b []byte) bool {
		return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE"
	}},
	{"audio/ogg", ".ogg", func(b []byte) bool { return len(b) > 4 && string(b[:4]) == "OggS" }},
	{"video/mp4", ".mp4", func(b []byte) bool {
		return len(b) > 12 && string(b[4:8]) == "ftyp"
	}},
	{"video/webm", ".webm", func(b []byte) bool {
		return len(b) > 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3
	}},
}

// isImage 用户发进来的那一侧只收图 —— 声音和视频是 bot 那边的事
func isImage(mime string) bool { return strings.HasPrefix(mime, "image/") }

// sniff 这堆字节是不是图, 是哪种.
func sniff(data []byte) (mime, ext string, ok bool) {
	for _, kind := range blobKinds {
		if kind.head(data) {
			return kind.mime, kind.ext, true
		}
	}
	return "", "", false
}

// save 存一张. 已经有同样内容的就直接用那一份.
func (b blobStore) save(name string, data []byte) (blobRef, error) {
	if b.dir == "" {
		return blobRef{}, fmt.Errorf("这台 OS 没配存图的地方")
	}
	mime, ext, ok := sniff(data)
	if ok && !isImage(mime) {
		ok = false
	}
	if !ok {
		// **收不下就明说**: 悄悄存一个界面渲染不出、模型也看不了的文件,
		// 比拒绝糟得多 —— 用户会以为图发出去了
		return blobRef{}, fmt.Errorf("%s 不是我们认得的图片格式（png / jpeg / webp / gif）", name)
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])[:32]
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return blobRef{}, err
	}
	path := filepath.Join(b.dir, id+ext)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return blobRef{}, err
		}
	}
	return blobRef{ID: id + ext, Name: name, Mime: mime, Path: path}, nil
}

/**
 * saveFile 用户发来的**文件**(不是图) —— 也按内容寻址落盘.
 *
 *	跟图共用一个目录, 但**不进 /blob 的读出口**: handleBlob 读文件头,
 *	认不出媒体的一律 404 —— "只放媒体过"的规矩没破. 文件存在这儿只为
 *	一件事: bot 用 read_file / run 打开它.
 */
func (b blobStore) saveFile(name string, data []byte) (blobRef, error) {
	if b.dir == "" {
		return blobRef{}, fmt.Errorf("这台 OS 没配存附件的地方")
	}
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return blobRef{}, err
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])[:32]
	path := filepath.Join(b.dir, id+safeExt(name))
	if _, err := os.Stat(path); err != nil {
		// 0644, **不带执行位**: 附件是给 bot 读的, 不是给谁跑的
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return blobRef{}, err
		}
	}
	return blobRef{ID: id + safeExt(name), Name: name, Path: path}, nil
}

// safeExt 扩展名跟着原名走, 但只收字母数字 —— 名字是别人起的,
// 里面可以藏任何东西; 认不出就 .bin, 反正 bot 会自己看内容
func safeExt(name string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	if len(ext) < 2 || len(ext) > 12 {
		return ".bin"
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ".bin"
		}
	}
	return ext
}

/**
 * fileNote 告诉 bot "有文件, 在这儿".
 *
 *	跟 imageNote 同一个道理: 给路径不给内容. 文本它自己 read_file,
 *	二进制让它先看清是什么再动 —— 直接读一个 zip 只会拿到一段乱码,
 *	然后它会花三轮去"修"一个不存在的编码问题.
 */
func fileNote(refs []blobRef) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n[用户还发了 %d 个文件]\n", len(refs))
	for _, ref := range refs {
		name := ref.Name
		if name != "" {
			name = "  （" + name + "）"
		}
		fmt.Fprintf(&b, "%s%s\n", ref.Path, name)
	}
	b.WriteString("文本类的用 read_file 打开；二进制的先用 run 看清它是什么（比如 file 命令），别硬读。")
	return b.String()
}

/**
 * handleBlob 把一张图发给界面.
 *
 *	两种取法:
 *	  ?id=<存进来的那张>      用户发的图
 *	  ?path=<绝对路径>        bot 看的那张(它在自己工作区里的图)
 *
 *	**path 那条只放图片过**: 先读文件头, 认不出是图的一律 404.
 *	这不是为了防住持有 token 的人(拿着 token 本来就能让 bot 读任何文件),
 *	而是为了这个口子**只有一种用途** —— 一个能吐任意文件的 HTTP 口,
 *	迟早会被当成别的东西用.
 */
func (s *ObserveServer) handleBlob(w http.ResponseWriter, r *http.Request) {
	var path string
	switch {
	case r.URL.Query().Get("id") != "":
		id := filepath.Base(r.URL.Query().Get("id")) // 只取文件名, 挡掉 ../
		if s.blobs.dir == "" {
			http.NotFound(w, r)
			return
		}
		path = filepath.Join(s.blobs.dir, id)
	case r.URL.Query().Get("path") != "":
		path = r.URL.Query().Get("path")
		if !filepath.IsAbs(path) {
			http.NotFound(w, r)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mime, _, ok := sniff(data)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	// 内容寻址的那些可以放心长缓存; 按路径取的不行(文件会被改)
	if r.URL.Query().Get("id") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(data)
}

/**
 * imageNote 告诉 bot "有图, 在这儿".
 *
 *	**给的是路径, 不是图本身**: 主模型认不认图是另一回事(见 engine/vision.go
 *	那段 —— 只认显式声明, 不按模型名猜). 给路径的话, 这条路对任何模型
 *	都是通的: 会看图的自己去 view_image, 不会看的至少知道"有一张图,
 *	我看不了", 而不是收到一段它读不懂的 base64.
 */
func imageNote(refs []blobRef, canSee bool) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n[用户还发了 %d 张图]\n", len(refs))
	for _, ref := range refs {
		name := ref.Name
		if name != "" {
			name = "  （" + name + "）"
		}
		fmt.Fprintf(&b, "%s%s\n", ref.Path, name)
	}
	if canSee {
		b.WriteString("看图用 view_image，**带一个具体问题** —— 拿回来的是一段转述，问什么才会翻出什么。")
	} else {
		b.WriteString("**这台机器没接会看图的模型，你看不了它**。别猜图里是什么，" +
			"直接说你看不了，让用户把关键内容打出来。")
	}
	return b.String()
}
