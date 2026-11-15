package main

import (
	"html"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Notes are stored as a small HTML subset:
//   blocks:  <p> <h1> <h2> <h3> <ul> <ol> <li>
//   inline:  <b> <i> <u> <s> <br>
// No attributes. Google Docs export HTML is converted into this subset.

var mergeRe = regexp.MustCompile(`</(b|i|u|s)><(b|i|u|s)>`)

// canon normalizes stored HTML so equal content compares equal.
func canon(s string) string {
	s = strings.TrimSpace(s)
	s = mergeRe.ReplaceAllStringFunc(s, func(m string) string {
		if m[2] == m[len(m)-2] {
			return ""
		}
		return m
	})
	for strings.HasSuffix(s, "<p><br></p>") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "<p><br></p>"))
	}
	return s
}

// textToHTML converts a plain-text note into the HTML subset.
func textToHTML(t string) string {
	var b strings.Builder
	for _, l := range strings.Split(norm(t), "\n") {
		if strings.TrimSpace(l) == "" {
			b.WriteString("<p><br></p>")
		} else {
			b.WriteString("<p>" + html.EscapeString(l) + "</p>")
		}
	}
	return canon(b.String())
}

// htmlToText returns the note's text, one line per block.
func htmlToText(h string) string {
	doc, err := xhtml.Parse(strings.NewReader(h))
	if err != nil {
		return h
	}
	var lines []string
	var cur strings.Builder
	open := false
	endLine := func() {
		if open {
			lines = append(lines, cur.String())
			cur.Reset()
			open = false
		}
	}
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		switch n.Type {
		case xhtml.TextNode:
			cur.WriteString(strings.ReplaceAll(n.Data, "\u00a0", " "))
			open = true
			return
		case xhtml.ElementNode:
			switch n.DataAtom {
			case atom.Style, atom.Script, atom.Head:
				return
			case atom.Br:
				if n.NextSibling != nil { // a trailing <br> only keeps an empty block open
					lines = append(lines, cur.String())
					cur.Reset()
				}
				open = true
				return
			}
		}
		block := n.Type == xhtml.ElementNode && isBlock(n)
		if block {
			endLine()
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			endLine()
		}
	}
	walk(doc)
	endLine()
	return norm(strings.Join(lines, "\n"))
}

func isBlock(n *xhtml.Node) bool {
	switch n.DataAtom {
	case atom.P, atom.Div, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Li,
		atom.Ul, atom.Ol, atom.Table, atom.Tbody, atom.Thead, atom.Tr, atom.Td, atom.Th,
		atom.Blockquote, atom.Pre, atom.Body, atom.Hr:
		return true
	}
	return false
}

var classRuleRe = regexp.MustCompile(`\.([\w-]+)\s*\{([^}]*)\}`)

func attr(n *xhtml.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

// sanitizeHTML converts arbitrary HTML (notably Google Docs exports, which put
// formatting in class rules) into the stored subset.
func sanitizeHTML(src string) string {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return textToHTML(src)
	}
	classes := map[string]string{}
	var body *xhtml.Node
	var find func(n *xhtml.Node)
	find = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Style && n.FirstChild != nil {
			for _, m := range classRuleRe.FindAllStringSubmatch(n.FirstChild.Data, -1) {
				classes[m[1]] += ";" + m[2]
			}
		}
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Body {
			body = n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if body == nil {
		return ""
	}
	styleOf := func(n *xhtml.Node) string {
		s := attr(n, "style")
		for _, c := range strings.Fields(attr(n, "class")) {
			s += ";" + classes[c]
		}
		return strings.ToLower(strings.ReplaceAll(s, " ", ""))
	}
	// Paragraph classes "title"/"subtitle" are Docs' Title/Subtitle styles.
	blockTag := func(n *xhtml.Node) string {
		switch n.DataAtom {
		case atom.H1:
			return "h1"
		case atom.H2:
			return "h2"
		case atom.H3, atom.H4, atom.H5, atom.H6:
			return "h3"
		}
		cl := " " + attr(n, "class") + " "
		if strings.Contains(cl, " title ") {
			return "h1"
		}
		if strings.Contains(cl, " subtitle ") {
			return "h2"
		}
		return "p"
	}

	var inline func(n *xhtml.Node) string
	inline = func(n *xhtml.Node) string {
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case xhtml.TextNode:
				b.WriteString(html.EscapeString(strings.ReplaceAll(c.Data, " ", " ")))
			case xhtml.ElementNode:
				if c.DataAtom == atom.Br {
					b.WriteString("<br>")
					continue
				}
				if c.DataAtom == atom.Style || c.DataAtom == atom.Script || c.DataAtom == atom.Img {
					continue
				}
				inner := inline(c)
				if inner == "" {
					continue
				}
				st := styleOf(c)
				bold := c.DataAtom == atom.B || c.DataAtom == atom.Strong ||
					strings.Contains(st, "font-weight:700") || strings.Contains(st, "font-weight:bold") ||
					strings.Contains(st, "font-weight:600") || strings.Contains(st, "font-weight:800")
				ital := c.DataAtom == atom.I || c.DataAtom == atom.Em || strings.Contains(st, "font-style:italic")
				und := c.DataAtom == atom.U || (strings.Contains(st, "underline") && c.DataAtom != atom.A && !insideLink(c))
				strike := c.DataAtom == atom.S || c.DataAtom == atom.Strike || c.DataAtom == atom.Del || strings.Contains(st, "line-through")
				if strike {
					inner = "<s>" + inner + "</s>"
				}
				if und {
					inner = "<u>" + inner + "</u>"
				}
				if ital {
					inner = "<i>" + inner + "</i>"
				}
				if bold {
					inner = "<b>" + inner + "</b>"
				}
				if isBlock(c) && c.NextSibling != nil {
					inner += "<br>"
				}
				b.WriteString(inner)
			}
		}
		return b.String()
	}

	var out strings.Builder
	var list func(n *xhtml.Node)
	list = func(n *xhtml.Node) {
		tag := "ul"
		if n.DataAtom == atom.Ol {
			tag = "ol"
		}
		// Docs exports each list run as its own <ul>; merge with a previous one.
		s := out.String()
		if strings.HasSuffix(s, "</"+tag+">") && n.PrevSibling != nil && n.PrevSibling.DataAtom == n.DataAtom {
			out.Reset()
			out.WriteString(strings.TrimSuffix(s, "</"+tag+">"))
		} else {
			out.WriteString("<" + tag + ">")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode {
				continue
			}
			if c.DataAtom == atom.Ul || c.DataAtom == atom.Ol {
				list(c)
				continue
			}
			// li: inline content, nested lists appended inside
			var nested []*xhtml.Node
			tmp := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Span, Data: "span"}
			for cc := c.FirstChild; cc != nil; {
				next := cc.NextSibling
				if cc.Type == xhtml.ElementNode && (cc.DataAtom == atom.Ul || cc.DataAtom == atom.Ol) {
					nested = append(nested, cc)
				}
				cc = next
			}
			txt := inline(withoutLists(c, tmp))
			if txt == "" {
				txt = "<br>"
			}
			out.WriteString("<li>" + txt)
			for _, nl := range nested {
				list(nl)
			}
			out.WriteString("</li>")
		}
		out.WriteString("</" + tag + ">")
	}

	var blocks func(n *xhtml.Node)
	blocks = func(n *xhtml.Node) {
		pending := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Span, Data: "span"}
		flush := func() {
			if pending.FirstChild != nil {
				if t := inline(pending); strings.TrimSpace(strings.ReplaceAll(t, "<br>", "")) != "" {
					out.WriteString("<p>" + t + "</p>")
				}
				pending = &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Span, Data: "span"}
			}
		}
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == xhtml.ElementNode && isBlock(c) {
				flush()
				switch {
				case c.DataAtom == atom.Ul || c.DataAtom == atom.Ol:
					list(c)
				case hasBlockChild(c):
					blocks(c)
				case c.DataAtom == atom.Hr:
				default:
					tag := blockTag(c)
					t := inline(c)
					if t == "" {
						t = "<br>"
					}
					out.WriteString("<" + tag + ">" + t + "</" + tag + ">")
				}
			} else if c.Type == xhtml.TextNode || c.Type == xhtml.ElementNode {
				n.RemoveChild(c)
				pending.AppendChild(c)
			}
			c = next
		}
		flush()
	}
	blocks(body)
	return canon(out.String())
}

func insideLink(n *xhtml.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.DataAtom == atom.A {
			return true
		}
	}
	return false
}

func hasBlockChild(n *xhtml.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && isBlock(c) && c.DataAtom != atom.Br {
			return true
		}
	}
	return false
}

// withoutLists returns a copy-ish container holding n's non-list children.
func withoutLists(n, tmp *xhtml.Node) *xhtml.Node {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if !(c.Type == xhtml.ElementNode && (c.DataAtom == atom.Ul || c.DataAtom == atom.Ol)) {
			n.RemoveChild(c)
			tmp.AppendChild(c)
		}
		c = next
	}
	return tmp
}
