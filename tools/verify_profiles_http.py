"""Verify that different profiles produce different .docx output.

Two modes:

    python verify_profiles_http.py <base-url> <profile-id> <out-dir>
        Upload → analyse → export one document under that profile, and record
        the measured body font/size and page margins.

    python verify_profiles_http.py --summarize <out-dir>
        Compare the recorded measurements; every profile must differ.

The measurements come from python-docx reading the exported file back, not from
what the profile claims — a profile that reports 宋体 12pt but renders 仿宋 16pt
is exactly the bug worth catching, and only reading the file finds it.
"""

import json
import os
import sys

BASE = None
DOC_TEXT = """关于开展安全生产大检查的通知

一、总体要求

坚持安全第一，认真负责，坚决防范各类事故发生。
"""


def upload_plan_export(base, profile, out_dir):
    import urllib.request
    import uuid

    # --- upload with the profile chosen ---
    boundary = uuid.uuid4().hex
    parts = []
    parts.append(f"--{boundary}\r\n".encode())
    parts.append(
        f'Content-Disposition: form-data; name="file"; filename="t.txt"\r\n'.encode()
    )
    parts.append(b"Content-Type: text/plain\r\n\r\n")
    parts.append(DOC_TEXT.encode("utf-8"))
    parts.append(f"\r\n--{boundary}\r\n".encode())
    parts.append(
        f'Content-Disposition: form-data; name="profile"\r\n\r\n{profile}\r\n'.encode()
    )
    parts.append(f"--{boundary}--\r\n".encode())
    body = b"".join(parts)

    req = urllib.request.Request(
        f"{base}/api/documents",
        data=body,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    with urllib.request.urlopen(req) as r:
        up = json.load(r)
    doc_id = up["doc_id"]
    applied = up.get("profile_id", "")
    if applied != profile:
        print(f"    ⚠ 服务端实际生效的配置是 {applied}（请求 {profile}）")

    # --- submit a plan covering every node ---
    nodes = up["nodes"]
    heading = next((n for n in nodes if n["text"].startswith("一、")), None)
    body_nodes = [n for n in nodes if n is not heading and n["id"] != nodes[0]["id"]]
    title = nodes[0]["id"]

    plan = {
        "elements": [{"role": "title", "node_id": title}],
        "preamble_node_ids": [],
        "sections": [
            {
                "id": "s1",
                "title": heading["text"] if heading else "总体要求",
                "level": 1,
                "heading_node_id": heading["id"] if heading else "",
                "body_node_ids": [n["id"] for n in body_nodes],
                "children": [],
            }
        ],
    }
    req = urllib.request.Request(
        f"{base}/api/documents/{doc_id}/plan",
        data=json.dumps(plan).encode(),
        headers={"Content-Type": "application/json"},
        method="PUT",
    )
    with urllib.request.urlopen(req) as r:
        json.load(r)

    # --- export ---
    req = urllib.request.Request(
        f"{base}/api/documents/{doc_id}/render",
        data=json.dumps({"format": "docx"}).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req) as r:
        rendered = json.load(r)

    req = urllib.request.Request(f"{base}/api/files/{rendered['file_id']}")
    with urllib.request.urlopen(req) as r:
        data = r.read()

    path = os.path.join(out_dir, f"{profile}.docx")
    with open(path, "wb") as fh:
        fh.write(data)
    return path


def measure(path):
    """Read back what the exported file actually contains.

    The body paragraph must be found by its *content*, not by position: the
    first non-empty paragraph is the 标题, whose typeface and size belong to a
    different role. An earlier version measured that paragraph and so reported
    方正小标宋简体 22pt as "the body" for 公文 — then, since the same role is
    黑体 16pt in both 通用 and 论文, concluded those two profiles were identical.
    The profiles were fine; the measurement was wrong.
    """
    from docx import Document

    d = Document(path)
    mm = lambda emu: round(emu / 36000, 1) if emu is not None else None

    W = "{http://schemas.openxmlformats.org/wordprocessingml/2006/main}"

    def font_of(run):
        rpr = run._element.find(f"{W}rPr")
        if rpr is None:
            return None, None
        fonts = rpr.find(f"{W}rFonts")
        eastasia = fonts.get(f"{W}eastAsia") if fonts is not None else None
        sz = rpr.find(f"{W}sz")
        half = int(sz.get(f"{W}val")) if sz is not None else None
        return eastasia, (half / 2 if half else None)

    body_font, body_pt = None, None
    for p in d.paragraphs:
        # The body is the paragraph that mentions the substantive text; the
        # fixture's body line starts with 坚持.
        if "坚持安全第一" not in p.text or not p.runs:
            continue
        body_font, body_pt = font_of(p.runs[0])
        break

    if body_font is None:
        raise SystemExit(f"{path}: 未找到正文段落，无法测量")

    sec = d.sections[0]
    return {
        "body_font": body_font,
        "body_pt": body_pt,
        "margins": [
            mm(sec.top_margin),
            mm(sec.bottom_margin),
            mm(sec.left_margin),
            mm(sec.right_margin),
        ],
    }


def main():
    global BASE
    argv = sys.argv[1:]

    if argv and argv[0] == "--summarize":
        out_dir = argv[1]
        rows = []
        for name in sorted(os.listdir(out_dir)):
            if not name.endswith(".docx"):
                continue
            m = measure(os.path.join(out_dir, name))
            rows.append((name[:-5], m))

        print(f"  {'配置':<10}{'正文字体':<14}{'字号':<8}页边距(上/下/左/右)")
        for name, m in rows:
            print(
                f"  {name:<10}{str(m['body_font']):<14}{str(m['body_pt']):<8}"
                f"{'/'.join(str(x) for x in m['margins'])}"
            )
        print()

        # A profile that renders but does not differ is the failure that matters.
        sigs = {}
        for name, m in rows:
            sig = (m["body_font"], m["body_pt"], tuple(m["margins"]))
            sigs.setdefault(sig, []).append(name)
        dupes = {k: v for k, v in sigs.items() if len(v) > 1}
        if dupes:
            print("  ✗ 以下配置产出完全相同：")
            for v in dupes.values():
                print(f"      {', '.join(v)}")
            sys.exit(1)
        print(f"  ✓ {len(rows)} 套配置排版互不相同")
        return

    base, profile, out_dir = argv[0], argv[1], argv[2]
    BASE = base
    os.makedirs(out_dir, exist_ok=True)
    path = upload_plan_export(base, profile, out_dir)
    m = measure(path)
    print(
        f"    正文={m['body_font']}/{m['body_pt']}pt "
        f"边距={'/'.join(str(x) for x in m['margins'])}mm"
    )


if __name__ == "__main__":
    main()