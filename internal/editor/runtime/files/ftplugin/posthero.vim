" posthero's request documents: # comments switch lines off.
if exists('b:did_ftplugin')
  finish
endif
let b:did_ftplugin = 1
setlocal commentstring=#\ %s comments=:#
" A # switches a line off, so Enter and o don't carry it to the next line;
" ### starts a section.
setlocal formatoptions-=t formatoptions-=c formatoptions-=r formatoptions-=o
let b:undo_ftplugin = 'setlocal commentstring< comments< formatoptions<'
