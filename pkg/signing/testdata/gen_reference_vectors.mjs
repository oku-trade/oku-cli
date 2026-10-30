// Usage: node gen_reference_vectors.mjs /path/to/node_modules/
// The signing test key below is a dummy key and must never be funded.
const modules=process.argv[2];
if (!modules) throw new Error('usage: node gen_reference_vectors.mjs /path/to/node_modules/');
const base=modules.replace(/\/?$/, '/');
const {encode}=await import(base+'@msgpack/msgpack/dist.esm/index.mjs').catch(()=>import(base+'@msgpack/msgpack/dist/index.js'));
const viem=await import(base+'viem/_esm/index.js');
const acc=await import(base+'viem/_esm/accounts/index.js');
const {keccak256,toHex}=viem;
function hashAction(action,vault,nonce){const a=encode(action);const n=new Uint8Array(8);new DataView(n.buffer).setBigUint64(0,nonce);let v;if(vault==null)v=new Uint8Array([0]);else{v=new Uint8Array(21);v[0]=1;v.set(viem.hexToBytes(vault.toLowerCase()),1)}const d=new Uint8Array(a.length+8+v.length);d.set(a);d.set(n,a.length);d.set(v,a.length+8);return {mp:toHex(a),h:keccak256(d)}}
const key='0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
const account=acc.privateKeyToAccount(key);
const cases=[
 ['{"type":"order","orders":[{"a":0,"b":true,"p":"50000","s":"0.001","r":false,"t":{"limit":{"tif":"Gtc"}},"c":"0x00000000000000000000000000000001"}],"grouping":"na","builder":{"b":"0xd637f2a36c1a3b37d57ef4c7022cb183d8922f2c","f":10}}',1727650000000n,null],
 ['{"type":"cancel","cancels":[{"a":110003,"o":123456789012}]}',1727650000001n,'0x1111111111111111111111111111111111111111'],
 ['{"type":"updateLeverage","asset":5,"isCross":true,"leverage":-300,"y":null,"big":4294967296,"neg":-40000,"s":"'+'x'.repeat(40)+'"}',42n,null],
];
const out=[];
for(const [j,nonce,vault] of cases){const {mp,h}=hashAction(JSON.parse(j),vault,nonce);
const sig=await account.signTypedData({domain:{name:'Exchange',version:'1',chainId:1337,verifyingContract:'0x0000000000000000000000000000000000000000'},types:{Agent:[{name:'source',type:'string'},{name:'connectionId',type:'bytes32'}]},primaryType:'Agent',message:{source:'a',connectionId:h}});
out.push({action_json:j,nonce:nonce.toString(),vault,msgpack:mp,connection_id:h,signature:sig});}
console.log(JSON.stringify({key,address:account.address,cases:out},null,1));
