#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Read a generated .docx back with python-docx and dump the measurements the
Go acceptance test asserts on.

    readback.py FILE.docx  >  measurements.json

The JSON deliberately reports BOTH the python-docx high-level view (font sizes,
alignment, table shape, page margins) and the raw w:pPr attribute values, since
python-docx does not expose the character-unit indentation attributes
(w:firstLineChars and friends) that GB/T 9704-2012's 「空二字」 depends on.
"""

import json
import sys

from docx import Document
from docx.oxml.ns import qn
from docx.shared import Length

EMU_PER_MM = 36000.0


def mm(emu):
    """EMU -> millimetres, using the 36000 EMU/mm the task pins."""
    if emu is None:
        return None
    return round(float(emu) / EMU_PER_MM, 6)


def local_attrs(el):
    """Attributes of an lxml element with the OOXML namespace stripped."""
    if el is None:
        return None
    return {k.split("}")[-1]: v for k, v in el.attrib.items()}


def child(el, tag):
    """Find a namespaced child, or None.

    lxml's find() is used rather than python-docx's attribute accessors
    because CT_PPr/CT_RPr only define accessors for a subset of the schema's
    children (pBdr among the missing ones), while find() always works.
    """
    if el is None:
        return None
    return el.find(qn(tag))


def child_attrs(el, tag):
    return local_attrs(child(el, tag))


def enum_name(value):
    return None if value is None else str(value)


def length_pt(value):
    if isinstance(value, Length):
        return round(value.pt, 6)
    return None


def run_info(run):
    el = run._element
    rpr = el.rPr
    fonts = None
    color = None
    if rpr is not None:
        rfonts = child(rpr, "w:rFonts")
        if rfonts is not None:
            fonts = {
                "eastAsia": rfonts.get(qn("w:eastAsia")),
                "ascii": rfonts.get(qn("w:ascii")),
                "hAnsi": rfonts.get(qn("w:hAnsi")),
            }
        node = child(rpr, "w:color")
        if node is not None:
            color = node.get(qn("w:val"))
    return {
        "text": run.text,
        "size_pt": length_pt(run.font.size),
        "bold": None if run.font.bold is None else bool(run.font.bold),
        "color": color,
        "fonts": fonts,
    }


def para_info(p):
    ppr = p._p.pPr
    pf = p.paragraph_format
    pbdr = child(ppr, "w:pBdr")
    borders = None
    if pbdr is not None:
        borders = {
            "top": child_attrs(pbdr, "w:top"),
            "bottom": child_attrs(pbdr, "w:bottom"),
        }
    return {
        "text": p.text,
        "style_name": None if p.style is None else p.style.name,
        "style_id": None if p.style is None else p.style.style_id,
        "alignment": enum_name(p.alignment),
        "line_spacing_pt": length_pt(pf.line_spacing),
        "line_spacing_rule": enum_name(pf.line_spacing_rule),
        "first_line_indent_pt": length_pt(pf.first_line_indent),
        "left_indent_pt": length_pt(pf.left_indent),
        "right_indent_pt": length_pt(pf.right_indent),
        # Raw pPr attribute views: python-docx exposes the point-valued twins
        # but not the character-unit attributes (w:firstLineChars), so the
        # acceptance test also needs the untouched XML.
        "pPr_spacing": child_attrs(ppr, "w:spacing"),
        "pPr_ind": child_attrs(ppr, "w:ind"),
        "pPr_jc": child_attrs(ppr, "w:jc"),
        "pBdr": borders,
        "runs": [run_info(r) for r in p.runs],
    }


def section_info(sec):
    return {
        "page_width_emu": int(sec.page_width),
        "page_height_emu": int(sec.page_height),
        "page_width_mm": mm(sec.page_width),
        "page_height_mm": mm(sec.page_height),
        "top_margin_emu": int(sec.top_margin),
        "bottom_margin_emu": int(sec.bottom_margin),
        "left_margin_emu": int(sec.left_margin),
        "right_margin_emu": int(sec.right_margin),
        "top_margin_mm": mm(sec.top_margin),
        "bottom_margin_mm": mm(sec.bottom_margin),
        "left_margin_mm": mm(sec.left_margin),
        "right_margin_mm": mm(sec.right_margin),
        "header_distance_mm": mm(sec.header_distance),
        "footer_distance_mm": mm(sec.footer_distance),
        "orientation": enum_name(sec.orientation),
    }


def main():
    if len(sys.argv) != 2:
        sys.stderr.write("usage: readback.py FILE.docx\n")
        return 2
    doc = Document(sys.argv[1])

    tables = []
    for table in doc.tables:
        tables.append(
            {
                "rows": len(table.rows),
                "cols": len(table.columns),
                "cells": [[cell.text for cell in row.cells] for row in table.rows],
            }
        )

    footer_paragraphs = []
    footer_error = None
    try:
        footer = doc.sections[0].footer
        footer_paragraphs = [para_info(p) for p in footer.paragraphs]
    except Exception as exc:  # pragma: no cover - reported, not raised
        footer_error = "%s: %s" % (type(exc).__name__, exc)

    result = {
        "sections": [section_info(s) for s in doc.sections],
        "paragraphs": [para_info(p) for p in doc.paragraphs],
        "tables": tables,
        "footer_paragraphs": footer_paragraphs,
        "footer_error": footer_error,
        "style_names": sorted(s.name for s in doc.styles),
    }
    json.dump(result, sys.stdout, ensure_ascii=False, indent=1)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
