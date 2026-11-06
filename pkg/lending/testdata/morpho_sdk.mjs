import { vaultV1Deposit, blueSupplyCollateral, blueSupplyCollateralBorrow, blueBorrow, blueRepay, blueRepayWithdrawCollateral, blueWithdrawCollateral } from "@morpho-org/morpho-sdk";
import { MarketParams } from "@morpho-org/blue-sdk";
const u = "0x1111111111111111111111111111111111111111";
const mp = new MarketParams({ loanToken: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", collateralToken: "0x4200000000000000000000000000000000000006", oracle: "0x663BECd10daE6C4A3Dcd89F1d76c1174199639B9", irm: "0x46415998764C29aB2a25CbeA6254146D50D22687", lltv: 860000000000000000n });
const m = { chainId: 8453, marketParams: mp };
const out = {
 market_id: mp.id,
 vault_deposit: vaultV1Deposit({vault:{chainId:8453,address:"0x050ce30b927da55177a4914ec73480238bad56f0",asset:"0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"},args:{amount:1000000n,maxSharePrice:1000300000000000000000000000n,recipient:u}}),
 supply_collateral: blueSupplyCollateral({market:m,args:{amount:10n**18n,onBehalf:u}}),
 supply_collateral_borrow: blueSupplyCollateralBorrow({market:m,args:{amount:10n**18n,borrowAmount:1000000000n,onBehalf:u,receiver:u,minSharePrice:999700000000000000000000n}}),
 borrow: blueBorrow({market:m,args:{amount:5000000n,receiver:u,minSharePrice:999700000000000000000000n}}),
 repay_assets: blueRepay({market:m,args:{amount:5000000n,transferAmount:5000000n,onBehalf:u,receiver:u,maxSharePrice:1000300000000000000000000n}}),
 repay_shares_withdraw: blueRepayWithdrawCollateral({market:m,args:{shares:4800000000000n,transferAmount:5100000n,withdrawAmount:10n**18n,onBehalf:u,receiver:u,maxSharePrice:1000300000000000000000000n}}),
 withdraw_collateral: blueWithdrawCollateral({market:m,args:{amount:10n**18n,onBehalf:u,receiver:u}}),
};
const j = Object.fromEntries(Object.entries(out).map(([k,v]) => [k, typeof v === "string" ? v : {to: v.to, data: v.data, value: String(v.value ?? 0n)}]));
console.log(JSON.stringify(j, null, 1));
