# gen_fixture.py - build a real .docx with python-docx so the Go parser can be
# checked against Word's own OOXML output rather than only hand-written XML.
import io
import sys

from docx import Document
from docx.shared import Pt
from PIL import Image

OUT = sys.argv[1]
PNG = sys.argv[2]

# A tiny real PNG, so the image relationship points at decodable bytes.
Image.new("RGB", (12, 8), (200, 30, 30)).save(PNG)

doc = Document()
doc.add_heading("一、总体要求", level=1)
doc.add_heading("（一）提高认识", level=2)

p = doc.add_paragraph()
p.add_run("  前导空格")
p.add_run("全角　空格")
p.add_run("“引号”")
p.add_run("省略号……")
p.add_run("尾随  ")

doc.add_paragraph("正文段落，包含\t制表符与……省略号。")

# List item via python-docx's list styles, which emit w:numPr.
doc.add_paragraph("列表项甲", style="List Number")

table = doc.add_table(rows=2, cols=2)
table.cell(0, 0).text = "序号"
table.cell(0, 1).text = "事项"
table.cell(1, 0).text = "1"
table.cell(1, 1).text = "甲　乙"

doc.add_page_break()
doc.add_paragraph("第二页正文。")
doc.add_picture(PNG)

doc.save(OUT)
print("wrote", OUT)
