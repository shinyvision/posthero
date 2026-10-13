" Vim syntax for posthero's request documents: the method and URL, query
" parameters and path variables, headers, the body (JSON, GraphQL, XML,
" text, forms) and the ### sections (scripts, auth, settings, docs).
if exists('b:current_syntax')
  finish
endif

let s:json = 0
silent! syntax include @postheroJSON syntax/json.vim
if exists('b:current_syntax')
  let s:json = 1
endif
unlet! b:current_syntax
silent! syntax include @postheroJS syntax/javascript.vim
unlet! b:current_syntax
silent! syntax include @postheroGraphQL syntax/graphql.vim
unlet! b:current_syntax
silent! syntax include @postheroXML syntax/xml.vim
unlet! b:current_syntax
silent! syntax include @postheroMarkdown syntax/markdown.vim
unlet! b:current_syntax

syntax case match

" {{variables}} and their dynamic and vault kinds, anywhere.
syntax match postheroVariable /{{[^{}]\+}}/ containedin=ALL contains=postheroDynamic
syntax match postheroDynamic /{{\zs\$[A-Za-z]\+\ze}}/ contained

" The head: everything up to the first blank line.
syntax region postheroHead start=/\%^/ end=/^\s*$/ contains=postheroRequestLine,postheroURLLine,postheroHeader,postheroOffHeader,postheroComment keepend
syntax match postheroRequestLine /^\u\+\s\+\S.*$/ contained contains=postheroMethod,postheroVariable,postheroQuery
syntax match postheroMethod /^\u\+\ze\s/ contained
syntax match postheroQuery /[?&][^=&#]*=\?/ contained contains=postheroVariable
syntax match postheroURLLine /^\s\+#\?\s*[?&:].*$/ contained contains=postheroOffParam,postheroParam,postheroPathVar,postheroVariable
syntax match postheroParam /^\s\+\zs[?&][^=]*=\?/ contained
syntax match postheroOffParam /^\s\+#.*$/ contained
syntax match postheroPathVar /^\s\+\zs:[^ =]\+/ contained
syntax match postheroHeader /^[!#$%&'*+.^_`|~0-9A-Za-z-]\+:/ contained
syntax match postheroOffHeader /^#.*$/ contained
syntax match postheroComment /^\s*\/\/.*$/ contained

" The body without a section: JSON, GraphQL, XML or text.
if s:json
  syntax region postheroBodyJSON start=/^\s*[{[]\("\|\s*$\|\s*{\|\s*\[\|\d\)/ end=/^\ze###\s/ contains=@postheroJSON,postheroVariable keepend
endif
syntax region postheroBodyGraphQL start=/^\s*\(query\|mutation\|subscription\|fragment\)\>/ end=/^\ze###\s/ contains=@postheroGraphQL,postheroVariable keepend
syntax region postheroBodyXML start=/^\s*<[?!A-Za-z]/ end=/^\ze###\s/ contains=@postheroXML,postheroVariable keepend

" Sections.
syntax match postheroSection /^###\s\+\S.*$/ contains=postheroSectionName
syntax match postheroSectionName /^###\s\+\zs\S\+/ contained
syntax region postheroScript matchgroup=postheroSection start=/^###\s\+\(pre-request\|prerequest\|pre\|tests\|test\|post-response\)\s*$/ end=/^\ze###\s/ contains=@postheroJS,postheroVariable keepend
syntax region postheroGraphQLSec matchgroup=postheroSection start=/^###\s\+\(graphql\|query\)\s*$/ end=/^\ze###\s/ contains=@postheroGraphQL,postheroVariable keepend
if s:json
  syntax region postheroJSONSec matchgroup=postheroSection start=/^###\s\+\(json\|variables\|vars\)\s*$/ end=/^\ze###\s/ contains=@postheroJSON,postheroVariable,postheroField,postheroOffField keepend
endif
syntax region postheroXMLSec matchgroup=postheroSection start=/^###\s\+\(xml\|html\)\s*$/ end=/^\ze###\s/ contains=@postheroXML,postheroVariable keepend
syntax region postheroJSSec matchgroup=postheroSection start=/^###\s\+\(javascript\|js\)\s*$/ end=/^\ze###\s/ contains=@postheroJS,postheroVariable keepend
syntax region postheroFields matchgroup=postheroSection start=/^###\s\+\(form\|multipart\|auth\|settings\|file\)\>.*$/ end=/^\ze###\s/ contains=postheroField,postheroOffField,postheroFile,postheroVariable keepend
syntax region postheroDocs matchgroup=postheroSection start=/^###\s\+\(description\|docs\)\s*$/ end=/^\ze###\s/ contains=@postheroMarkdown keepend
syntax region postheroEnv matchgroup=postheroSection start=/^###\s\+\(environment\|collection\|globals\)\>.*$/ end=/^\ze###\s/ contains=postheroField,postheroOffField,postheroVariable,postheroNote keepend
syntax match postheroField /^\s*[^#:="{}[\][:space:]][^:=]*\ze\s*[:=]/ contained
syntax match postheroOffField /^\s*#.*$/ contained
syntax match postheroNote /^\s*#\s\+\(team\|only yours\|shared\).*$/ contained
syntax match postheroFile /@\S\+/ contained

highlight default link postheroVariable Identifier
highlight default link postheroDynamic Special
highlight default link postheroMethod Statement
highlight default link postheroQuery Type
highlight default link postheroParam Type
highlight default link postheroOffParam Comment
highlight default link postheroPathVar Type
highlight default link postheroHeader Type
highlight default link postheroOffHeader Comment
highlight default link postheroComment Comment
highlight default link postheroSection Title
highlight default link postheroSectionName Title
highlight default link postheroField Type
highlight default link postheroOffField Comment
highlight default link postheroNote Comment
highlight default link postheroFile String

let b:current_syntax = 'posthero'
