package main

import (
	"strings"
	"testing"

	"github.com/neox-os/neox-os/agent"
)

// 交给被约束进程的环境 —— **钉的是"谁把什么喂给了它"**.
//
// "算对了但接错线"这类错栽过三次, 每一次都只有真机能抓到:
//
//	commandPath   算出来了, 调用点还写着旧的固定 PATH
//	插话框架话     写好了, 塞进了 LLM 根本不读的 history
//	对话清单       算出来了, 排除项传的是**上一段**的线头
//
// 三次的共同点: 被测的函数都是对的, 错的是喂给它的东西. 内联的代码没有接缝,
// 测试就够不着 —— 所以把 spawnEnv 拎出来, 让这条缝可测.

// 凭据绝不进被约束的进程 —— 这条错了就是把钥匙交出去了
func TestSpawnEnvNeverCarriesCredentials(t *testing.T) {
	t.Setenv("NEOX_API_KEY", "sk-绝密")
	env := spawnEnv("/agentwork", "干活", nil, "", "")
	for k, v := range env {
		if strings.Contains(k, "API_KEY") || strings.Contains(v, "sk-绝密") {
			t.Fatalf("凭据漏进了被约束的进程: %s=%s", k, v)
		}
	}
}

// 接续哪一段, 就排除哪一段 —— **不是"上一段"**.
//
// 排除项如果传成 threadOfCurrent(上一段的线头),
// /新 之后它还留着, 于是刚聊完的那段被当成"当前段"剔掉了 ——
// 用户问"昨天那个", 清单里恰好少了他最可能问的那段.
func TestSpawnEnvExcludesTheThreadBeingJoined(t *testing.T) {
	convs := []agent.Conversation{
		{Thread: "t1", Title: "刚聊完的那段", Turns: 3, LastAt: 200},
		{Thread: "t2", Title: "更早那段", Turns: 9, LastAt: 100},
	}
	// 全新对话: 两段都该在
	fresh := spawnEnv("/w", "新话题", convs, "", "")["NEOX_RECENT"]
	for _, want := range []string{"刚聊完的那段", "更早那段"} {
		if !strings.Contains(fresh, want) {
			t.Fatalf("全新对话该列全部, 缺 %q:\n%s", want, fresh)
		}
	}
	// 接续 t2: 只排除 t2
	resumed := spawnEnv("/w", "接着聊", convs, "t2", "")["NEOX_RECENT"]
	if strings.Contains(resumed, "更早那段") {
		t.Fatalf("接续的那段不该再列一遍:\n%s", resumed)
	}
	if !strings.Contains(resumed, "刚聊完的那段") {
		t.Fatalf("别的段被误删了:\n%s", resumed)
	}
}

// 任务原文必须原样送到 —— 它是这一轮全部工作的依据
func TestSpawnEnvCarriesTheTaskVerbatim(t *testing.T) {
	task := "把 报表/七月订单.csv 转成 Excel，未付的标红"
	if got := spawnEnv("/w", task, nil, "", "")["NEOX_TASK"]; got != task {
		t.Fatalf("任务原文被改了:\n要 %q\n得 %q", task, got)
	}
}

// 旋钮要真的透传 —— 不透传的话在外面 export 是没有反应的,
// 而那种"设了没效果"最难查(我自己在 NEOX_CTX_BYTES 上栽过一次)
func TestSpawnEnvCarriesCorrectionsInTheirOwnKey(t *testing.T) {
	corr := "- 纠正：「账本用分不要用元」"
	env := spawnEnv("/w", "x", nil, "", corr)
	if env["NEOX_CORRECTIONS"] != corr {
		t.Fatalf("纠正没进 NEOX_CORRECTIONS: %q", env["NEOX_CORRECTIONS"])
	}
	if strings.Contains(env["NEOX_RECENT"], "用分") {
		t.Fatal("纠正串进了对话清单 —— 两个键混了, 提示词会对不上层")
	}
}

func TestSpawnEnvForwardsKnobs(t *testing.T) {
	t.Setenv("NEOX_MAX_STEPS", "7")
	t.Setenv("NEOX_CTX_BYTES", "30000")
	t.Setenv("NEOX_MAX_OUTPUT_TOKENS", "120")
	env := spawnEnv("/w", "x", nil, "", "")
	for k, want := range map[string]string{
		"NEOX_MAX_STEPS": "7", "NEOX_CTX_BYTES": "30000", "NEOX_MAX_OUTPUT_TOKENS": "120",
	} {
		if env[k] != want {
			t.Fatalf("%s 没透传: 要 %q 得 %q", k, want, env[k])
		}
	}
}
