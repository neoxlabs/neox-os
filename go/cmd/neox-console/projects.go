package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

/**
 * projectNames — 项目的显示名. 目录路径 → 人起的名字.
 *
 *	项目本身没有实体: 它是"一个工作区目录 + 在里面干活的人"推导出来的.
 *	目录名(basename)是磁盘上的事实, 不该为了好看去改它 —— 改目录名会
 *	牵动 worktree、内核给的写能力、bot 提示词里的路径, 每一处都可能断.
 *
 *	所以显示名单独存: 人看的是这份, 机器认的还是路径. 跟名册(bots.json)
 *	分开是刻意的 —— 名册是平的 []savedBot, 混进一张 map 老文件就读不动了.
 */
type projectNames struct {
	mu   sync.Mutex
	path string
}

func newProjectNames() *projectNames {
	return &projectNames{path: filepath.Join(neoxHome(), "projects.json")}
}

func (p *projectNames) load() map[string]string {
	raw, err := os.ReadFile(p.path)
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	if json.Unmarshal(raw, &out) != nil {
		return map[string]string{}
	}
	return out
}

// set 记一个显示名. 空名字 = 撤掉别名, 回到目录名.
func (p *projectNames) set(path, name string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("项目路径是空的")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.load()
	name = strings.TrimSpace(name)
	if name == "" {
		delete(current, path)
	} else {
		current[path] = name
	}
	return writeMap(p.path, current)
}

/**
 * roomOwners — 谁是群主. 房间名 → bot 名.
 *
 *	BOT 组成一个群需要明确的群主, 否则房间缺少稳定的协调者.
 *	群主 = **开这个房间的那个**(招人时自动成组的领头), 跟交办只许
 *	一层是同一个模型: 用户 → 领头 → 帮手, 链子到此为止.
 *
 *	存和 projectNames 同一套写法: 一张小 map, 落在自己的文件里.
 */
type roomOwners struct {
	mu   sync.Mutex
	path string
}

func newRoomOwners() *roomOwners {
	return &roomOwners{path: filepath.Join(neoxHome(), "rooms.json")}
}

func (r *roomOwners) load() map[string]string {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	if json.Unmarshal(raw, &out) != nil {
		return map[string]string{}
	}
	return out
}

func (r *roomOwners) set(thread, owner string) error {
	thread = strings.TrimSpace(thread)
	if thread == "" {
		return fmt.Errorf("房间名是空的")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.load()
	owner = strings.TrimSpace(owner)
	if owner == "" {
		delete(current, thread)
	} else {
		current[thread] = owner
	}
	return writeMap(r.path, current)
}

// writeMap 原子落一张小 map —— projectNames / roomOwners 共用
func writeMap(path string, data map[string]string) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
