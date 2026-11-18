// Keep ASCII keyboard actions; use the browser editing engine for unmapped Unicode.
export async function appendText(page, text) {
  for (const character of text) {
    if (character >= " " && character <= "~") await page.keyboard.type(character);
    else if (!await page.evaluate(text => document.execCommand("insertText", false, text), character))
      throw new Error("browser Unicode editing command refused");
  }
}

export async function replaceContentText(page, control, text) {
  await control.click();
  await control.press("ControlOrMeta+A");
  if (!await page.evaluate(text => document.execCommand("insertText", false, text), text))
    throw new Error("browser content replacement command refused");
}

export async function focusParagraphEnd(page, paragraph) {
  await paragraph.click();
  await page.keyboard.press("End");
  return paragraph.evaluate(element => {
    const selection = window.getSelection();
    const inside = Boolean(selection?.anchorNode && element.contains(selection.anchorNode));
    if (!inside) return { inside: false, collapsed: Boolean(selection?.isCollapsed), atEnd: false };
    const beforeCaret = document.createRange();
    beforeCaret.selectNodeContents(element);
    beforeCaret.setEnd(selection.anchorNode, selection.anchorOffset);
    return { inside, collapsed: selection.isCollapsed, atEnd: beforeCaret.toString() === element.textContent };
  });
}
