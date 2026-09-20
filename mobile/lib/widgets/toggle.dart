import 'package:flutter/material.dart';

import '../theme.dart';

/// 开关 —— 38×22, 白钮 18. 客户端 .tgl 那个尺寸.
/// 系统那个 Switch 在 Android 上是 52 宽还带个大水波, 一页放三个
/// 就成了这一页最重的东西
class NxToggle extends StatelessWidget {
  const NxToggle({super.key, required this.on, required this.onChanged});
  final bool on;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) => GestureDetector(
        onTap: () => onChanged(!on),
        behavior: HitTestBehavior.opaque,
        child: AnimatedContainer(
          duration: const Duration(milliseconds: 140),
          width: 40,
          height: 24,
          padding: const EdgeInsets.all(2),
          decoration: BoxDecoration(
            color: on ? NX.accent : NX.fill2,
            borderRadius: BorderRadius.circular(NX.rPill),
          ),
          child: AnimatedAlign(
            duration: const Duration(milliseconds: 140),
            curve: Curves.easeOutBack,
            alignment: on ? Alignment.centerRight : Alignment.centerLeft,
            child: Container(
              width: 20,
              height: 20,
              decoration: BoxDecoration(
                color: Colors.white,
                shape: BoxShape.circle,
                boxShadow: [
                  BoxShadow(
                      color: Colors.black.withValues(alpha: .3),
                      blurRadius: 2,
                      offset: const Offset(0, 1)),
                ],
              ),
            ),
          ),
        ),
      );
}

