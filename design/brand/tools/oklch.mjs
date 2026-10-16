// OKLCH <-> sRGB helpers (Björn Ottosson's OKLab) and WCAG 2 contrast.
export const hex2rgb = h => [1,3,5].map(i => parseInt(h.slice(i,i+2),16)/255);
const lin = c => c <= 0.04045 ? c/12.92 : ((c+0.055)/1.055)**2.4;
const delin = c => c <= 0.0031308 ? 12.92*c : 1.055*c**(1/2.4)-0.055;
export function oklch(h){ const [r,g,b]=hex2rgb(h).map(lin);
 const l=Math.cbrt(0.4122214708*r+0.5363325363*g+0.0514459929*b), m=Math.cbrt(0.2119034982*r+0.6806995451*g+0.1073969566*b), s=Math.cbrt(0.0883024619*r+0.2817188376*g+0.6299787005*b);
 const L=0.2104542553*l+0.7936177850*m-0.0040720468*s, a=1.9779984951*l-2.4285922050*m+0.4505937099*s, bb=0.0259040371*l+0.7827717662*m-0.8086757660*s;
 return [L, Math.hypot(a,bb), (Math.atan2(bb,a)*180/Math.PI+360)%360]; }
export function fromOklch(L,C,H){ const a=C*Math.cos(H*Math.PI/180), b=C*Math.sin(H*Math.PI/180);
 const l=(L+0.3963377774*a+0.2158037573*b)**3, m=(L-0.1055613458*a-0.0638541728*b)**3, s=(L-0.0894841775*a-1.2914855480*b)**3;
 let rgb=[4.0767416621*l-3.3077115913*m+0.2309699292*s, -1.2684380046*l+2.6097574011*m-0.3413193965*s, -0.0041960863*l-0.7034186147*m+1.7076147010*s];
 return rgb; }
export function inGamut(rgb){return rgb.every(c=>c>=-1e-4&&c<=1+1e-4);}
export function toHex(L,C,H){ // reduce chroma until in gamut
 let c=C, rgb=fromOklch(L,c,H); while(!inGamut(rgb) && c>0){c-=0.002; rgb=fromOklch(L,c,H);} 
 return '#'+rgb.map(x=>Math.round(Math.min(1,Math.max(0,delin(Math.min(1,Math.max(0,x)))))*255).toString(16).padStart(2,'0')).join(''); }
export const relLum = h => { const [r,g,b]=hex2rgb(h).map(lin); return 0.2126*r+0.7152*g+0.0722*b; };
export const contrast = (a,b) => { const [x,y]=[relLum(a),relLum(b)].sort((p,q)=>q-p); return (x+0.05)/(y+0.05); };
