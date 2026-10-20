// The pieces of CodeMirror 6 the dashboard uses, bundled into one script (window.CM) by build.sh. No CDN: the bundle
// is built here and served with the rest of the app.
export { EditorState, Compartment } from '@codemirror/state';
export { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, placeholder, drawSelection, ViewPlugin, Decoration, MatchDecorator } from '@codemirror/view';
export { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
export { syntaxHighlighting, HighlightStyle } from '@codemirror/language';
export { markdown, markdownLanguage } from '@codemirror/lang-markdown';
export { autocompletion, completionKeymap, closeBrackets } from '@codemirror/autocomplete';
export { tags } from '@lezer/highlight';
