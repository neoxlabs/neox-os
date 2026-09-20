"""
png —— 画一张**内容只有看得见才答得出**的图.

    视觉这条路最容易假过: 给一张纯色图问"什么颜色", 六选一, 蒙也能蒙对;
    而模型不看图、只顺着话头答"红色"看起来跟真看见了一模一样.

    所以图上画两位数字: 蒙中的概率 1/100. 答对了就是真看见了.

    不依赖 PIL —— 测试台不该为了画个图去装一堆东西. PNG 的最小形态
    就是 zlib 压一遍加几个块, 三十行写得完.
"""
import struct
import zlib

# 5x7 点阵, 够用就行
FONT = {
    "0": ["01110", "10001", "10011", "10101", "11001", "10001", "01110"],
    "1": ["00100", "01100", "00100", "00100", "00100", "00100", "01110"],
    "2": ["01110", "10001", "00001", "00010", "00100", "01000", "11111"],
    "3": ["11111", "00010", "00100", "00010", "00001", "10001", "01110"],
    "4": ["00010", "00110", "01010", "10010", "11111", "00010", "00010"],
    "5": ["11111", "10000", "11110", "00001", "00001", "10001", "01110"],
    "6": ["00110", "01000", "10000", "11110", "10001", "10001", "01110"],
    "7": ["11111", "00001", "00010", "00100", "01000", "01000", "01000"],
    "8": ["01110", "10001", "10001", "01110", "10001", "10001", "01110"],
    "9": ["01110", "10001", "10001", "01111", "00001", "00010", "01100"],
}


def digits_png(text, scale=16, pad=16):
    """把几个数字画成一张黑底白字的 PNG, 返回原始字节"""
    glyphs = [FONT[c] for c in text]
    gw, gh = 5, 7
    gap = 2
    w = pad * 2 + (gw * len(glyphs) + gap * (len(glyphs) - 1)) * scale
    h = pad * 2 + gh * scale
    # 白底黑字 —— 比黑底更像人随手截的一张图
    rows = [[255] * (w * 3) for _ in range(h)]
    for gi, glyph in enumerate(glyphs):
        x0 = pad + gi * (gw + gap) * scale
        for ry, line in enumerate(glyph):
            for rx, bit in enumerate(line):
                if bit != "1":
                    continue
                for dy in range(scale):
                    y = pad + ry * scale + dy
                    for dx in range(scale):
                        x = x0 + rx * scale + dx
                        rows[y][x * 3:x * 3 + 3] = [16, 16, 16]

    raw = b"".join(b"\x00" + bytes(r) for r in rows)

    def chunk(tag, data):
        return (struct.pack(">I", len(data)) + tag + data
                + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF))

    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw, 9))
            + chunk(b"IEND", b""))
