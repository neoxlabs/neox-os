package com.neox.sense

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView

/** 最小 UI: 填采集入口地址和 token, 起停服务. 这个应用不需要别的界面 */
class MainActivity : Activity() {
    override fun onCreate(b: Bundle?) {
        super.onCreate(b)
        val prefs = getSharedPreferences("neox_sense", Context.MODE_PRIVATE)
        val url = EditText(this).apply {
            hint = "采集入口 http://10.0.2.2:8787"
            setText(prefs.getString("url", "http://10.0.2.2:8787"))
        }
        val token = EditText(this).apply {
            hint = "token"
            setText(prefs.getString("token", ""))
        }
        val status = TextView(this)
        val start = Button(this).apply {
            text = "开始采集"
            setOnClickListener {
                startForegroundService(Intent(context, SenseService::class.java).apply {
                    putExtra("url", url.text.toString())
                    putExtra("token", token.text.toString())
                })
                status.text = "采集中"
            }
        }
        val stop = Button(this).apply {
            text = "停止"
            setOnClickListener {
                stopService(Intent(context, SenseService::class.java))
                status.text = "已停止"
            }
        }
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(40, 80, 40, 40)
            addView(url); addView(token); addView(start); addView(stop); addView(status)
        })

        // 带参数启动就直接开跑.
        //
        // 两个用途: ① adb 驱动的真机验证(安卓不允许从后台起前台服务,
        // 必须由一个前台 Activity 来起) ② 真实部署时用一条深链配置好
        intent?.getStringExtra("url")?.let { u ->
            url.setText(u)
            intent.getStringExtra("token")?.let { token.setText(it) }
            val dwell = intent.getLongExtra("dwellMs", 0L)
            startForegroundService(Intent(this, SenseService::class.java).apply {
                putExtra("url", u)
                putExtra("token", token.text.toString())
                if (dwell > 0) putExtra("dwellMs", dwell)
            })
            status.text = "采集中(由启动参数)"
        }

        val perms = mutableListOf(Manifest.permission.ACCESS_FINE_LOCATION,
            Manifest.permission.READ_PHONE_STATE)
        if (Build.VERSION.SDK_INT >= 33) perms += Manifest.permission.POST_NOTIFICATIONS
        requestPermissions(perms.toTypedArray(), 1)
    }
}
