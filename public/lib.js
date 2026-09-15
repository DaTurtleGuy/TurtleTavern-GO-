/**
 * Add all the libraries that you want to expose to the client here.
 * They are bundled and exposed by Webpack in the /lib.js file.
 */
import lodashMerge from 'lodash/merge';
import lodashSet from 'lodash/set';
import lodashGet from 'lodash/get';
import lodashThrottle from 'lodash/throttle';
import lodashRange from 'lodash/range';

const lodash = Object.freeze({
    merge: lodashMerge,
    set: lodashSet,
    get: lodashGet,
    throttle: lodashThrottle,
    range: lodashRange,
});
import Fuse from 'fuse.js';
import DOMPurify from 'dompurify';
import hljs from 'highlight.js/lib/core';
import hljsJson from 'highlight.js/lib/languages/json';
import hljsJavascript from 'highlight.js/lib/languages/javascript';
import hljsPython from 'highlight.js/lib/languages/python';
import hljsXml from 'highlight.js/lib/languages/xml';
import hljsCss from 'highlight.js/lib/languages/css';
import hljsBash from 'highlight.js/lib/languages/bash';

hljs.registerLanguage('json', hljsJson);
hljs.registerLanguage('javascript', hljsJavascript);
hljs.registerLanguage('python', hljsPython);
hljs.registerLanguage('xml', hljsXml);
hljs.registerLanguage('css', hljsCss);
hljs.registerLanguage('bash', hljsBash);
import localforage from 'localforage';
import Handlebars from 'handlebars';
import css from '@adobe/css-tools';
import Bowser from 'bowser';
import DiffMatchPatch from 'diff-match-patch';
import { isProbablyReaderable, Readability } from '@mozilla/readability';
import SVGInject from '@iconfu/svg-inject';
import showdown from 'showdown';
import moment from 'moment';
import seedrandom from 'seedrandom';
import * as Popper from '@popperjs/core';
import droll from 'droll';
import morphdom from 'morphdom';
import { toggle as slideToggle } from 'slidetoggle';
import yaml from 'yaml';
import { createToken as chevrotainCreateToken, Lexer as chevrotainLexer, CstParser as chevrotainCstParser } from 'chevrotain';

const chevrotain = Object.freeze({
    createToken: chevrotainCreateToken,
    Lexer: chevrotainLexer,
    CstParser: chevrotainCstParser,
});
import { gzipSync, gzip } from 'fflate';

/**
 * Expose the libraries to the 'window' object.
 * Needed for compatibility with old extensions.
 * Note: New extensions are encouraged to import the libraries directly from lib.js.
 */
export function initLibraryShims() {
    if (!window) {
        return;
    }
    if (!('Fuse' in window)) {
        // @ts-ignore
        window.Fuse = Fuse;
    }
    if (!('DOMPurify' in window)) {
        // @ts-ignore
        window.DOMPurify = DOMPurify;
    }
    if (!('hljs' in window)) {
        // @ts-ignore
        window.hljs = hljs;
    }
    if (!('localforage' in window)) {
        // @ts-ignore
        window.localforage = localforage;
    }
    if (!('Handlebars' in window)) {
        // @ts-ignore
        window.Handlebars = Handlebars;
    }
    if (!('diff_match_patch' in window)) {
        // @ts-ignore
        window.diff_match_patch = DiffMatchPatch;
    }
    if (!('SVGInject' in window)) {
        // @ts-ignore
        window.SVGInject = SVGInject;
    }
    if (!('showdown' in window)) {
        // @ts-ignore
        window.showdown = showdown;
    }
    if (!('moment' in window)) {
        // @ts-ignore
        window.moment = moment;
    }
    if (!('Popper' in window)) {
        // @ts-ignore
        window.Popper = Popper;
    }
    if (!('droll' in window)) {
        // @ts-ignore
        window.droll = droll;
    }
}

export default {
    lodash,
    Fuse,
    DOMPurify,
    hljs,
    localforage,
    Handlebars,
    css,
    Bowser,
    DiffMatchPatch,
    Readability,
    isProbablyReaderable,
    SVGInject,
    showdown,
    moment,
    seedrandom,
    Popper,
    droll,
    morphdom,
    slideToggle,
    yaml,
    chevrotain,
    gzipSync,
    gzip,
};

export {
    lodash,
    Fuse,
    DOMPurify,
    hljs,
    localforage,
    Handlebars,
    css,
    Bowser,
    DiffMatchPatch,
    Readability,
    isProbablyReaderable,
    SVGInject,
    showdown,
    moment,
    seedrandom,
    Popper,
    droll,
    morphdom,
    slideToggle,
    yaml,
    chevrotain,
    gzipSync,
    gzip,
};
