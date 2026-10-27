#!/usr/bin/env python3
"""Render isolated demo artwork and fill only empty images on the exact 20 fixtures.

Uses Pillow and Python's standard library, never reads application configuration.
A SQLite online backup precedes the single images-only transaction. Existing
images are always preserved, including those from an earlier run of this script.
"""
import argparse
from datetime import datetime, timezone
from io import BytesIO
import json
import math
from pathlib import Path
import sqlite3
import subprocess
from urllib.request import urlopen

from PIL import Image, ImageDraw, ImageFilter, ImageFont

from seed_frontend_demo import NAMES, PREFIX

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / "db" / "dujiao.db"
OUT = ROOT / "uploads" / "demo-orbit-20261025"
SCALE = 2
SIZE = 960
PALETTES = [
    ((108, 91, 222), (239, 143, 179), (239, 234, 255)),
    ((32, 147, 139), (119, 198, 180), (226, 246, 241)),
    ((54, 112, 219), (111, 176, 240), (229, 239, 255)),
    ((211, 129, 60), (236, 184, 108), (255, 242, 221)),
]
LABELS = ["INSPIRATION CANVAS", "ICON DESIGN KIT", "COLOR PRESETS", "MOTION SLIDES", "VISUAL LIBRARY",
          "FOCUS WORKSPACE", "PROJECT BOARD", "MEETING NOTES", "KNOWLEDGE MAP", "DAILY PLANNER",
          "API WORKBENCH", "CODE SNIPPETS", "TERMINAL THEMES", "DATA VISUALIZATION", "AUTOMATION LAB",
          "LANGUAGE PLAN", "DESIGN FOUNDATIONS", "CODING WORKBOOK", "READING JOURNAL", "LEARNING COLLECTION"]


def mix(a, b, t):
    return tuple(round(x * (1 - t) + y * t) for x, y in zip(a, b))


class Art:
    def __init__(self, index):
        self.index = index
        self.accent, self.secondary, self.base = PALETTES[(index - 1) // 5]
        bg = Image.new("RGB", (240, 240))
        pixels = bg.load()
        for y in range(240):
            for x in range(240):
                glow = math.exp(-((x - 180) ** 2 + (y - 55) ** 2) / 9000)
                shade = math.exp(-((x - 35) ** 2 + (y - 195) ** 2) / 12000)
                pixels[x, y] = mix(mix((250, 251, 254), self.base, .65), self.secondary, .15 * glow + .10 * shade)
        self.im = bg.resize((SIZE * SCALE, SIZE * SCALE), Image.Resampling.BICUBIC).convert("RGBA")
        self.d = ImageDraw.Draw(self.im)
        self.ink = (45, 58, 83)
        self.muted = (157, 170, 192)
        # Quiet orbital lines frame the illustration without becoming a UI.
        self.ellipse((30, 95, 930, 995), None, (*self.accent, 24), 1)
        self.ellipse((-100, 15, 1060, 1175), None, (*self.accent, 15), 1)
        self.text((84, 73), ["CREATIVE TOOLS", "PRODUCTIVITY", "DEVELOPER TOOLS", "LEARNING RESOURCES"][(index - 1) // 5], 16, self.accent)
        self.text((84, 858), LABELS[index - 1], 24, self.ink)
        self.line([(84, 907), (876, 907)], (*self.accent, 42), 1)
        self.text((84, 921), "DIGITAL COLLECTION  /  DEMO ARTWORK", 10, self.muted)

    def box(self, box):
        return tuple(round(v * SCALE) for v in box)

    def rr(self, box, fill="white", radius=22, outline=None, width=1):
        self.d.rounded_rectangle(self.box(box), radius=radius * SCALE, fill=fill, outline=outline, width=width * SCALE)

    def ellipse(self, box, fill, outline=None, width=1):
        self.d.ellipse(self.box(box), fill=fill, outline=outline, width=width * SCALE)

    def line(self, points, fill, width=3):
        self.d.line([(int(x * SCALE), int(y * SCALE)) for x, y in points], fill=fill, width=width * SCALE, joint="curve")

    def poly(self, points, fill):
        self.d.polygon([(int(x * SCALE), int(y * SCALE)) for x, y in points], fill=fill)

    def text(self, xy, text, size=20, fill=None):
        self.d.text((xy[0] * SCALE, xy[1] * SCALE), text, fill=fill or self.ink, font=ImageFont.load_default(size=size * SCALE))

    def arc(self, box, start, end, fill, width):
        self.d.arc(self.box(box), start=start, end=end, fill=fill, width=width * SCALE)

    def card(self, box, radius=24, fill=(255, 255, 255, 245)):
        shadow = Image.new("RGBA", self.im.size)
        sd = ImageDraw.Draw(shadow)
        shifted = (box[0], box[1] + 15, box[2], box[3] + 15)
        sd.rounded_rectangle(self.box(shifted), radius * SCALE, fill=(*self.accent, 32))
        self.im = Image.alpha_composite(self.im, shadow.filter(ImageFilter.GaussianBlur(18 * SCALE)))
        self.d = ImageDraw.Draw(self.im)
        self.rr(box, fill, radius, (255, 255, 255, 245), 2)

    def window(self, box=(122, 200, 838, 770), dark=False):
        self.card(box, 28, (32, 45, 71, 255) if dark else (255, 255, 255, 240))
        x, y, r, b = box
        for i, color in enumerate([self.secondary, mix(self.accent, (255, 255, 255), .5), self.accent]):
            self.ellipse((x + 24 + i * 20, y + 24, x + 33 + i * 20, y + 33), color)
        self.line([(x + 20, y + 55), (r - 20, y + 55)], (71, 86, 110) if dark else (232, 236, 245), 1)
        self.rr((r - 100, y + 20, r - 24, y + 36), (72, 88, 117) if dark else self.base, 7)

    def bars(self, x, y, lengths, gap=22, color=None):
        for i, length in enumerate(lengths):
            self.rr((x, y + i * gap, x + length, y + i * gap + 7), color or (209, 219, 233), 4)

    def sphere(self, x, y, radius, color=None):
        color = color or self.accent
        for r in range(radius, 0, -1):
            t = 1 - r / radius
            cx, cy = x - t * radius * .25, y - t * radius * .30
            self.ellipse((cx - r, cy - r, cx + r, cy + r), mix(color, (255, 255, 255), t * .86))

    def badge(self, x, y, kind="spark"):
        self.card((x - 62, y - 62, x + 62, y + 62), 32, (*self.accent, 255))
        if kind == "check":
            self.line([(x - 27, y), (x - 7, y + 20), (x + 30, y - 22)], "white", 10)
        elif kind == "play":
            self.poly([(x - 16, y - 29), (x + 30, y), (x - 16, y + 29)], "white")
        elif kind == "code":
            self.line([(x - 14, y - 23), (x - 35, y), (x - 14, y + 23)], "white", 7)
            self.line([(x + 14, y - 23), (x + 35, y), (x + 14, y + 23)], "white", 7)
        else:
            self.poly([(x, y - 40), (x + 11, y - 11), (x + 40, y), (x + 11, y + 11), (x, y + 40), (x - 11, y + 11), (x - 40, y), (x - 11, y - 11)], "white")

    def draw(self):
        n, a, b = self.index, self.accent, self.secondary
        if n == 1:  # Vector canvas and tangible pen.
            self.window((120, 194, 815, 754))
            for x in range(188, 775, 36):
                for y in range(295, 695, 36):
                    self.ellipse((x, y, x + 2, y + 2), (220, 224, 238))
            self.rr((290, 323, 643, 642), self.base, 8, a, 2)
            self.ellipse((340, 366, 560, 586), b)
            self.poly([(470, 596), (600, 365), (670, 596)], a)
            for x, y in [(290, 323), (643, 323), (290, 642), (643, 642)]:
                self.rr((x - 7, y - 7, x + 7, y + 7), "white", 1, a, 2)
            self.card((152, 288, 226, 632), 20)
            for j in range(5):
                self.ellipse((175, 313 + j * 57, 202, 340 + j * 57), a if j == 1 else self.base)
            self.line([(684, 716), (801, 465)], a, 28)
            self.poly([(669, 752), (673, 707), (698, 719)], self.ink)
            self.badge(778, 260)
        elif n == 2:  # Grid of actual, distinct icon silhouettes.
            self.window((120, 186, 840, 774))
            for j in range(9):
                x, y = 171 + (j % 3) * 214, 295 + (j // 3) * 144
                self.rr((x, y, x + 185, y + 119), self.base if j != 4 else a, 22)
                c = "white" if j == 4 else a
                cx, cy = x + 92, y + 59
                if j == 0:
                    self.poly([(cx, cy - 33), (cx + 36, cy), (cx + 22, cy + 34), (cx - 22, cy + 34), (cx - 36, cy)], c)
                elif j == 1:
                    self.rr((cx - 38, cy - 22, cx + 38, cy + 31), None, 11, c, 6)
                    self.ellipse((cx - 14, cy - 11, cx + 14, cy + 17), None, c, 5)
                elif j == 2:
                    self.poly([(cx + 10, cy - 39), (cx - 26, cy + 5), (cx, cy + 5), (cx - 8, cy + 40), (cx + 29, cy - 7), (cx + 3, cy - 7)], c)
                elif j == 3:
                    self.arc((cx - 34, cy - 34, cx + 34, cy + 34), 15, 340, c, 9)
                    self.ellipse((cx - 9, cy - 9, cx + 9, cy + 9), c)
                elif j == 4:
                    self.poly([(cx, cy - 37), (cx + 12, cy - 12), (cx + 37, cy), (cx + 12, cy + 12), (cx, cy + 37), (cx - 12, cy + 12), (cx - 37, cy), (cx - 12, cy - 12)], c)
                elif j == 5:
                    self.rr((cx - 35, cy - 27, cx + 35, cy + 23), None, 10, c, 5)
                    self.poly([(cx - 24, cy + 21), (cx - 24, cy + 39), (cx, cy + 21)], c)
                elif j == 6:
                    self.poly([(cx - 30, cy - 27), (cx + 34, cy), (cx - 30, cy + 27)], c)
                elif j == 7:
                    self.line([(cx - 32, cy + 15), (cx - 9, cy + 35), (cx + 34, cy - 31)], c, 9)
                else:
                    self.ellipse((cx - 28, cy - 28, cx + 28, cy + 28), None, c, 7)
                    self.line([(cx + 22, cy + 22), (cx + 42, cy + 42)], c, 8)
        elif n == 3:  # Landscape editing with an obvious before/after division.
            self.window((108, 194, 852, 742))
            self.rr((143, 280, 817, 620), (207, 211, 236), 16)
            self.ellipse((560, 305, 662, 407), (255, 224, 176))
            self.poly([(143, 582), (321, 365), (492, 566), (650, 393), (817, 572), (817, 620), (143, 620)], (169, 151, 201))
            self.poly([(143, 620), (320, 463), (465, 591), (624, 450), (817, 620)], a)
            self.rr((143, 280, 422, 620), (220, 226, 236, 110), 16)
            self.line([(422, 280), (422, 620)], "white", 3)
            self.ellipse((403, 432, 441, 470), "white")
            self.line([(414, 442), (414, 459)], a, 2)
            self.line([(430, 442), (430, 459)], a, 2)
            for j, color in enumerate([a, b, (229, 169, 116), (108, 174, 167), (93, 126, 178)]):
                self.rr((170 + j * 127, 652, 274 + j * 127, 691), color, 10)
            self.card((579, 695, 829, 805), 20)
            for j in range(3):
                yy = 721 + 25 * j
                self.line([(604, yy), (804, yy)], (216, 220, 234), 4)
                xx = 648 + 53 * (j % 2)
                self.ellipse((xx - 7, yy - 7, xx + 7, yy + 7), a)
        elif n == 4:
            self.card((178, 170, 866, 690), 26, (*self.base, 255))
            self.window((108, 235, 800, 755))
            self.text((152, 328), "IDEAS IN MOTION", 27, a)
            self.bars(153, 383, [215, 168, 191], 20)
            self.sphere(598, 455, 111, a)
            self.ellipse((455, 398, 740, 510), None, b, 12)
            self.poly([(593, 420), (641, 453), (593, 485)], "white")
            for j in range(5):
                self.rr((151 + j * 122, 612, 253 + j * 122, 694), self.base, 10)
                self.rr((164 + j * 122, 626, 235 + j * 122, 634), a if j == 0 else b, 4)
            self.badge(773, 730, "play")
        elif n == 5:
            self.card((120, 195, 578, 675), 28)
            self.rr((160, 239, 537, 529), a, 18)
            self.ellipse((220, 290, 420, 490), None, "white", 35)
            self.poly([(355, 286), (464, 491), (405, 491), (298, 286)], b)
            self.bars(164, 573, [235, 155], 26)
            self.card((525, 318, 844, 747), 25)
            self.text((565, 372), "Aa", 101, a)
            self.bars(566, 496, [200, 141], 23)
            for j, c in enumerate([a, b, self.base]):
                self.rr((560 + j * 83, 578, 628 + j * 83, 685), c, 13)
            self.card((154, 702, 479, 795), 18)
            self.text((183, 730), "FORM  /  COLOR  /  TYPE", 17, a)
        elif n == 6:
            self.window((121, 198, 838, 756))
            self.ellipse((275, 292, 655, 672), None, self.base, 30)
            self.arc((275, 292, 655, 672), 270, 558, a, 30)
            self.text((369, 438), "25:00", 62, a)
            self.text((412, 524), "FOCUS", 20, self.muted)
            self.rr((383, 584, 548, 626), self.base, 21)
            self.poly([(452, 595), (475, 605), (452, 616)], a)
            self.card((672, 407, 861, 685), 22)
            for j in range(4):
                self.ellipse((698, 442 + j * 54, 714, 458 + j * 54), b if j else a)
                self.bars(727, 447 + j * 54, [103], color=self.base)
            self.sphere(169, 695, 60, b)
        elif n == 7:
            self.window((94, 183, 866, 776))
            for col in range(3):
                x = 129 + col * 242
                self.rr((x, 281, x + 218, 731), self.base, 17)
                self.rr((x + 18, 304, x + 110, 315), a if col != 1 else b, 5)
                for row in range(3 if col == 0 else 2):
                    y = 348 + row * 117
                    self.card((x + 13, y, x + 205, y + 99), 12)
                    self.rr((x + 27, y + 17, x + 80, y + 25), a if row == 0 else b, 4)
                    self.bars(x + 27, y + 41, [146, 102], 17)
                    self.ellipse((x + 160, y + 68, x + 178, y + 86), self.secondary)
            self.badge(779, 710, "check")
        elif n == 8:
            self.card((195, 176, 752, 763), 26, (*self.base, 255))
            self.card((144, 222, 697, 803), 26)
            self.text((193, 277), "MEETING NOTES", 30, a)
            self.bars(194, 337, [370, 284, 333], 23)
            for j in range(4):
                y = 447 + j * 65
                self.rr((195, y, 219, y + 24), self.base, 6, b, 1)
                if j < 2:
                    self.line([(199, y + 12), (206, y + 19), (217, y + 5)], a, 3)
                self.bars(244, y + 8, [304 - j * 29], color=self.muted)
            self.card((610, 393, 846, 629), 24, (*a, 255))
            for j in range(15):
                x = 635 + j * 13
                h = 15 + 49 * abs(math.sin(j * .9))
                self.rr((x, 512 - h, x + 6, 512 + h), "white", 3)
            self.rr((722, 698, 750, 795), a, 8)
            self.poly([(722, 795), (750, 795), (736, 825)], self.ink)
        elif n == 9:
            nodes = [(478, 474), (221, 290), (733, 288), (184, 617), (748, 630), (478, 745)]
            for x, y in nodes[1:]:
                self.line([(478, 474), (x, y)], mix(a, (255, 255, 255), .46), 5)
            for j, (x, y) in enumerate(nodes):
                if j == 0:
                    self.card((x - 120, y - 115, x + 120, y + 115), 35, (*a, 255))
                    self.poly([(x - 76, y - 51), (x - 6, y - 30), (x - 6, y + 62), (x - 76, y + 40)], "white")
                    self.poly([(x + 76, y - 51), (x + 6, y - 30), (x + 6, y + 62), (x + 76, y + 40)], self.base)
                else:
                    self.card((x - 90, y - 69, x + 90, y + 69), 22)
                    self.rr((x - 58, y - 37, x - 24, y - 3), self.base, 8)
                    self.bars(x - 58, y + 16, [110, 76], 19)
                    self.ellipse((x + 46, y - 33, x + 62, y - 17), b)
        elif n == 10:
            self.window((114, 194, 780, 754))
            self.text((155, 285), "WEEKLY PLAN", 26, a)
            for r in range(5):
                for c in range(7):
                    x, y = 156 + c * 82, 357 + r * 66
                    self.rr((x, y, x + 65, y + 50), a if (r, c) == (2, 3) else self.base, 11)
                    self.ellipse((x + 26, y + 21, x + 35, y + 30), "white" if (r, c) == (2, 3) else mix(a, (255, 255, 255), .65))
            self.card((660, 438, 845, 799), 29)
            self.rr((719, 454, 786, 464), self.base, 5)
            self.text((689, 505), "TODAY", 20, a)
            for j in range(4):
                self.rr((683, 553 + j * 49, 822, 589 + j * 49), self.base if j else a, 9)
            self.rr((725, 778, 781, 783), self.muted, 2)
        elif n == 11:
            self.window((103, 194, 856, 766))
            self.rr((140, 290, 817, 346), self.base, 12)
            self.text((159, 306), "GET", 22, a)
            self.bars(246, 316, [385], color=mix(a, (255, 255, 255), .55))
            for j in range(3):
                self.rr((145, 398 + j * 81, 386, 459 + j * 81), self.base, 12)
                self.bars(166, 419 + j * 81, [153, 100], 15)
            self.rr((432, 390, 812, 681), (39, 57, 88), 18)
            self.text((459, 413), "{", 55, b)
            for j, length in enumerate([203, 147, 220, 126, 174]):
                self.rr((492 + (j % 2) * 21, 480 + j * 30, 492 + length, 489 + j * 30), b if j % 2 else (152, 207, 200), 4)
            self.text((459, 615), "}", 39, b)
            self.line([(367, 700), (538, 700), (538, 733), (728, 733)], a, 4)
            self.badge(785, 264, "code")
        elif n == 12:
            self.window((181, 166, 862, 689), dark=True)
            self.window((106, 276, 773, 789), dark=True)
            self.rr((137, 363, 294, 748), (44, 62, 90), 13)
            for j in range(6):
                self.rr((155, 391 + j * 49, 266, 418 + j * 49), a if j == 1 else (67, 86, 118), 6)
            for j, length in enumerate([171, 279, 195, 235, 132, 264, 174, 224, 151]):
                x, y = 338 + (j % 3) * 21, 382 + j * 37
                self.rr((x, y, x + length, y + 10), [b, (165, 154, 225), (150, 205, 190)][j % 3], 4)
            self.badge(779, 717, "code")
        elif n == 13:
            self.window((195, 177, 863, 655), dark=True)
            self.window((109, 316, 785, 780), dark=True)
            self.text((151, 421), ">_", 112, b)
            self.bars(156, 590, [449, 326, 390], 35, (131, 181, 214))
            self.rr((443, 456, 468, 534), (154, 208, 193), 3)
            self.card((609, 667, 870, 811), 22)
            for j, c in enumerate([a, (171, 148, 227), (117, 186, 166), (230, 180, 130)]):
                self.ellipse((632 + j * 57, 714, 674 + j * 57, 756), c)
        elif n == 14:
            self.window((104, 190, 857, 774))
            self.rr((140, 284, 477, 569), self.base, 18)
            for j, height in enumerate([77, 125, 101, 176, 207, 154]):
                self.rr((165 + j * 47, 535 - height, 194 + j * 47, 535), a if j % 2 == 0 else b, 7)
            self.ellipse((534, 304, 788, 558), None, self.base, 44)
            self.arc((534, 304, 788, 558), 270, 504, a, 44)
            self.arc((534, 304, 788, 558), 145, 218, b, 44)
            self.rr((140, 604, 817, 727), self.base, 17)
            pts = [(165 + j * 58, 687 - (math.sin(j * .78) + j * .1) * 34) for j in range(11)]
            self.line(pts, a, 6)
            for x, y in pts[::2]:
                self.ellipse((x - 6, y - 6, x + 6, y + 6), "white", a, 3)
        elif n == 15:
            self.window((109, 198, 850, 768))
            points = [(249, 380), (481, 380), (713, 380), (713, 625), (481, 625), (249, 625)]
            self.line(points, mix(a, (255, 255, 255), .60), 7)
            for j, (x, y) in enumerate(points):
                self.card((x - 77, y - 72, x + 77, y + 72), 23, (*a, 255) if j in (0, 5) else (255, 255, 255, 255))
                c = "white" if j in (0, 5) else a
                if j == 0:
                    self.poly([(x - 16, y - 31), (x + 33, y), (x - 16, y + 31)], c)
                elif j == 5:
                    self.line([(x - 28, y), (x - 6, y + 22), (x + 32, y - 24)], c, 9)
                else:
                    self.ellipse((x - 27, y - 27, x + 27, y + 27), None, c, 7)
                    for k in range(8):
                        angle = k * math.pi / 4
                        self.line([(x + 29 * math.cos(angle), y + 29 * math.sin(angle)), (x + 43 * math.cos(angle), y + 43 * math.sin(angle))], c, 7)
            self.text((365, 507), "WORKFLOW", 24, a)
        elif n == 16:
            self.ellipse((191, 201, 780, 790), None, mix(a, (255, 255, 255), .7), 2)
            self.arc((273, 208, 700, 784), 60, 302, mix(a, (255, 255, 255), .7), 2)
            self.card((124, 273, 586, 546), 37, (*a, 255))
            self.poly([(204, 535), (196, 597), (285, 538)], a)
            self.text((182, 339), "Hello", 77, "white")
            self.bars(187, 454, [240], color=(244, 204, 159))
            self.card((437, 497, 840, 724), 34)
            self.poly([(718, 712), (786, 768), (784, 710)], "white")
            self.text((489, 550), "Hola", 69, a)
            self.bars(493, 654, [183], color=self.base)
            self.sphere(742, 283, 66, b)
        elif n == 17:
            self.window((111, 196, 847, 771))
            for x in range(151, 810, 44):
                self.line([(x, 284), (x, 715)], self.base, 1)
            for y in range(285, 716, 43):
                self.line([(151, y), (809, y)], self.base, 1)
            self.ellipse((176, 333, 429, 586), b)
            self.poly([(473, 319), (658, 632), (293, 632)], a)
            self.rr((569, 387, 775, 594), None, 1, (72, 95, 122), 15)
            self.card((164, 665, 797, 749), 15)
            for j in range(17):
                x = 187 + j * 35
                self.line([(x, 677), (x, 704 if j % 4 else 724)], a, 2)
            self.badge(776, 257)
        elif n == 18:
            self.card((166, 191, 777, 774), 26, (*self.base, 255))
            self.card((127, 230, 723, 813), 25)
            for j in range(9):
                self.ellipse((149, 267 + j * 58, 166, 284 + j * 58), self.base)
            self.text((204, 284), "BUILD & LEARN", 28, a)
            self.rr((203, 348, 671, 603), (42, 54, 77), 20)
            self.text((233, 370), "</>", 75, b)
            self.bars(237, 490, [321, 245, 280], 31, (169, 191, 215))
            for j in range(3):
                self.rr((206 + j * 152, 651, 336 + j * 152, 742), self.base, 13)
                self.line([(235 + j * 152, 696), (252 + j * 152, 711), (283 + j * 152, 678)], a, 6)
            self.badge(773, 653, "code")
        elif n == 19:
            self.card((133, 233, 827, 735), 25)
            self.poly([(173, 278), (466, 312), (466, 704), (173, 667)], self.base)
            self.poly([(487, 312), (789, 278), (789, 667), (487, 704)], (255, 248, 234))
            self.line([(477, 315), (477, 703)], b, 4)
            for j in range(10):
                self.line([(202, 335 + j * 31), (429, 360 + j * 31)], (214, 184, 151), 4)
                self.line([(522, 360 + j * 31), (753, 335 + j * 31)], (218, 191, 158), 4)
            self.poly([(671, 290), (711, 285), (711, 465), (691, 444), (671, 466)], a)
            self.card((238, 720, 722, 804), 22)
            self.rr((270, 751, 689, 772), self.base, 10)
            self.rr((270, 751, 545, 772), a, 10)
            self.sphere(181, 236, 52, b)
        else:
            # A bundle, not another numbered card: three mediums and a learning path.
            self.window((277, 175, 855, 581))
            self.rr((314, 263, 465, 529), self.base, 14)
            self.bars(334, 291, [108, 78, 98, 65], 30)
            pts = [(512, 477), (576, 425), (632, 449), (692, 343), (782, 296)]
            self.line(pts, a, 7)
            for x, y in pts:
                self.ellipse((x - 10, y - 10, x + 10, y + 10), "white", a, 4)
            self.card((126, 380, 437, 778), 22, (*a, 255))
            self.line([(162, 401), (162, 750)], (244, 196, 140), 3)
            self.text((192, 440), "LEARN", 35, "white")
            self.text((192, 487), "CREATE", 35, "white")
            self.ellipse((203, 575, 359, 731), None, (249, 223, 184), 17)
            self.poly([(271, 609), (323, 653), (271, 697)], "white")
            self.card((472, 616, 841, 790), 22)
            for j in range(3):
                self.ellipse((507 + j * 94, 651, 562 + j * 94, 706), mix(a, (255, 255, 255), j * .22))
                self.rr((503 + j * 94, 723, 570 + j * 94, 733), self.base, 5)
        return self.im.convert("RGB").resize((SIZE, SIZE), Image.Resampling.LANCZOS)


def fetch(base, path):
    with urlopen(base + path, timeout=15) as response:
        if response.status != 200:
            raise RuntimeError(f"HTTP {response.status}: {path}")
        return response.headers.get_content_type(), response.read()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8088")
    parser.add_argument("--server-pid", type=int, default=28432)
    args = parser.parse_args()
    if args.base_url != "http://127.0.0.1:8088":
        parser.error("This isolated fixture tool only accesses the local server on port 8088")
    opened = subprocess.check_output(["/usr/sbin/lsof", "-nP", "-a", "-p", str(args.server_pid), "-Fpcn", str(DB)], text=True)
    if f"p{args.server_pid}\n" not in opened or f"n{DB}\n" not in opened or "cserver\n" not in opened:
        raise RuntimeError("Expected running server does not have the exact database open")
    slugs = [PREFIX + f"product-{i:02d}" for i in range(1, 21)]
    conn = sqlite3.connect(DB.as_uri() + "?mode=rw", uri=True, timeout=30, isolation_level=None)
    try:
        if conn.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='products'").fetchone()[0]:
            raise RuntimeError("Unreviewed product triggers; refusing writes")
        rows = conn.execute("SELECT id,slug,title_json,images,deleted_at FROM products WHERE slug GLOB ? ORDER BY slug", (PREFIX + "*",)).fetchall()
        if [r[1] for r in rows] != slugs or any(r[4] is not None for r in rows):
            raise RuntimeError("Exact 20 active fixture slugs required")
        for i, row in enumerate(rows):
            if json.loads(row[2]).get("en-US") != "[TEST] " + NAMES[i][2]:
                raise RuntimeError(f"Unexpected fixture title: {row[1]}")
            if not isinstance(json.loads(row[3]), list):
                raise RuntimeError("images must be a JSON array")
        pending = [r for r in rows if not json.loads(r[3])]
        OUT.mkdir(parents=True, exist_ok=True)
        rendered = []
        for row in rows:
            i = slugs.index(row[1]) + 1
            target = OUT / f"product-{i:02d}.webp"
            if row not in pending:
                # Preserve all existing images and their backing files on reruns.
                continue
            if target.exists():
                raise RuntimeError(f"Refusing to overwrite an existing artwork file: {target}")
            image = Art(i).draw()
            image.save(target, "WEBP", quality=93, method=6)
            path = "/uploads/demo-orbit-20261025/" + target.name
            mime, body = fetch(args.base_url, path)
            if mime != "image/webp" or body != target.read_bytes():
                raise RuntimeError(f"Published artwork mismatch: {path}")
            with Image.open(BytesIO(body)) as check:
                if check.size != (960, 960) or check.format != "WEBP":
                    raise RuntimeError(f"Invalid artwork: {path}")
                check.load()
            rendered.append((row, target, path))
            print(json.dumps({"rendered": i, "bytes": target.stat().st_size, "http": 200, "content_type": mime}), flush=True)
        sheet = Image.new("RGB", (1440, 1192), (245, 247, 250))
        sd = ImageDraw.Draw(sheet)
        for row in rows:
            i = slugs.index(row[1]) + 1
            target = OUT / f"product-{i:02d}.webp"
            if target.exists():
                with Image.open(target) as image:
                    thumb = image.resize((272, 272), Image.Resampling.LANCZOS)
                x, y = 8 + ((i - 1) % 5) * 288, 8 + ((i - 1) // 5) * 298
                sheet.paste(thumb, (x, y))
                sd.text((x + 4, y + 277), f"{i:02d}  {LABELS[i - 1]}", font=ImageFont.load_default(size=12), fill=(51, 65, 85))
        sheet_path = Path("/tmp/demo-orbit-20261025-contact-sheet.jpg")
        sheet.save(sheet_path, quality=94)
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
        backup_path = DB.with_name(DB.name + f".images-demo-orbit-20261025-{stamp}.bak")
        backup_path.touch(mode=0o600, exist_ok=False)
        with sqlite3.connect(backup_path) as backup:
            conn.backup(backup)
        print(json.dumps({"backup": str(backup_path)}), flush=True)
        conn.execute("BEGIN IMMEDIATE")
        changed = 0
        try:
            cursor = conn.execute("SELECT * FROM products ORDER BY id")
            columns = [c[0] for c in cursor.description]
            before = cursor.fetchall()
            image_index = columns.index("images")
            id_index = columns.index("id")
            expected = {}
            for row, target, path in rendered:
                value = json.dumps([path], separators=(",", ":")).encode("utf-8")
                count = conn.execute("UPDATE products SET images=? WHERE id=? AND slug=? AND images=? AND deleted_at IS NULL", (value, row[0], row[1], row[3])).rowcount
                if count != 1:
                    raise RuntimeError("Fixture changed concurrently; rolling back")
                changed += count
                expected[row[0]] = value
            after = conn.execute("SELECT * FROM products ORDER BY id").fetchall()
            expected_rows = []
            for row in before:
                wanted = list(row)
                if row[id_index] in expected:
                    wanted[image_index] = expected[row[id_index]]
                expected_rows.append(tuple(wanted))
            if after != expected_rows or conn.total_changes != changed:
                raise RuntimeError("Unexpected change beyond the exact allowed images fields")
            conn.execute("COMMIT")
        except BaseException:
            conn.execute("ROLLBACK")
            raise
        verified = []
        for row in rows:
            mime, body = fetch(args.base_url, "/api/v1/public/products/" + row[1])
            payload = json.loads(body)
            images = payload["data"]["images"]
            i = slugs.index(row[1]) + 1
            path = f"/uploads/demo-orbit-20261025/product-{i:02d}.webp"
            if row in pending or json.loads(row[3]) == [path]:
                if images != [path]:
                    raise RuntimeError(f"Public API images mismatch: {row[1]}")
                mime, content = fetch(args.base_url, path)
                if mime != "image/webp" or content != (OUT / f"product-{i:02d}.webp").read_bytes():
                    raise RuntimeError("Public image HTTP verification failed")
                verified.append(row[1])
        sizes = [target.stat().st_size for _, target, _ in rendered]
        print(json.dumps({"changed_products": changed, "skipped_existing_images": len(rows) - len(pending), "verified_public_products_and_images": len(verified), "dimensions": [960, 960], "file_size_bytes_min": min(sizes) if sizes else None, "file_size_bytes_max": max(sizes) if sizes else None, "contact_sheet": str(sheet_path), "backup": str(backup_path), "only_images_changed": True}, indent=2))
    finally:
        conn.close()


if __name__ == "__main__":
    main()
