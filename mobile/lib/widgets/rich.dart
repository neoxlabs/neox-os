import 'package:flutter/material.dart';

import '../theme.dart';

/// 把 Markdown 画出来 —— **只认说话时真会用到的那几个**。
///
/// ── 为什么非做不可 ──
///
/// 之前整条消息是一段纯文本，模型写的 `**重点**` 原样显示成一串星号。
/// 用户看到的是「渲染不完整」，而真相是**一个字都没渲染**。
///
/// ── 为什么不装一个 Markdown 包 ──
///
/// 装一个的代价不是那几百 KB，是**它会把所有语法都渲染出来** ——
/// 标题、引用、水平线、表格。而说话方式那一层刚刚明确要求它别用这些
/// （「不分点、不加小标题，那是文档的样子」）。渲染得越全，越是在
/// 鼓励它写成一份文档。
///
/// 所以只认三样：**粗体**、`行内代码`、以及行首的 `- ` 列表符号。
/// 别的原样显示 —— **原样比猜错强**：一段没识别的语法看着是丑，
/// 而一段猜错的语法会把内容吃掉。
///
/// ── 流式那根光标要能挂上去 ──
///
/// 返回的是 spans 而不是一个 Widget，调用方才能在尾巴上再接一个
/// WidgetSpan（那根还在长的竖条）。
List<InlineSpan> richSpans(String text, TextStyle base) {
  final out = <InlineSpan>[];
  final buf = StringBuffer();

  void flush() {
    if (buf.isEmpty) return;
    out.add(TextSpan(text: buf.toString(), style: base));
    buf.clear();
  }

  var i = 0;
  var lineStart = true;
  while (i < text.length) {
    final c = text[i];

    // 行首的 "- " → 一个圆点。**只认行首**：句子中间的减号是减号
    if (lineStart && c == '-' && i + 1 < text.length && text[i + 1] == ' ') {
      flush();
      out.add(TextSpan(text: '· ', style: base.copyWith(color: NX.text3)));
      i += 2;
      lineStart = false;
      continue;
    }
    lineStart = c == '\n';

    // **粗体**
    if (c == '*' && i + 1 < text.length && text[i + 1] == '*') {
      final end = text.indexOf('**', i + 2);
      // 没有配对的收尾 —— **原样显示**，别把后面半篇都吞成粗体：
      // 流式输出时前半截随时可能是"还没写完"
      if (end > i + 2) {
        flush();
        out.add(TextSpan(
            text: text.substring(i + 2, end),
            style: base.copyWith(fontWeight: NX.wBold)));
        i = end + 2;
        continue;
      }
    }

    // `行内代码`
    if (c == '`') {
      final end = text.indexOf('`', i + 1);
      if (end > i + 1 && !text.substring(i + 1, end).contains('\n')) {
        flush();
        out.add(TextSpan(
            text: text.substring(i + 1, end),
            style: base.copyWith(
                fontFamily: 'monospace',
                fontSize: base.fontSize! - 1,
                color: NX.cyan)));
        i = end + 1;
        continue;
      }
    }

    buf.write(c);
    i++;
  }
  flush();
  return out;
}
