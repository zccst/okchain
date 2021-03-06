package keeper

import (
	"fmt"
	"sort"
	"time"

	"github.com/pkg/errors"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/okex/okchain/x/dex/types"
	"github.com/okex/okchain/x/params"
)

// Keeper maintains the link to data storage and exposes getter/setter methods for the various parts of the state machine
type Keeper struct {
	supplyKeeper      SupplyKeeper
	feeCollectorName  string // name of the FeeCollector ModuleAccount
	tokenKeeper       TokenKeeper
	stakingKeeper     StakingKeeper // The reference to the staking keeper  to check whether proposer is  validator
	bankKeeper        BankKeeper    // The reference to the bank keeper to check whether proposer can afford  proposal deposit
	govKeeper         GovKeeper     // The reference to the gov keeper to handle proposal
	storeKey          sdk.StoreKey
	tokenPairStoreKey sdk.StoreKey
	paramSubspace     params.Subspace // The reference to the Paramstore to get and set gov modifiable params
	cdc               *codec.Codec    // The wire codec for binary encoding/decoding.
	cache             *Cache          // reset cache data in BeginBlock
}

// NewKeeper creates new instances of the token Keeper
func NewKeeper(feeCollectorName string, supplyKeeper SupplyKeeper, dexParamsSubspace params.Subspace, tokenKeeper TokenKeeper,
	stakingKeeper StakingKeeper, bankKeeper BankKeeper, storeKey, tokenPairStoreKey sdk.StoreKey, cdc *codec.Codec) Keeper {

	k := Keeper{
		tokenKeeper:       tokenKeeper,
		feeCollectorName:  feeCollectorName,
		supplyKeeper:      supplyKeeper,
		stakingKeeper:     stakingKeeper,
		bankKeeper:        bankKeeper,
		paramSubspace:     dexParamsSubspace.WithKeyTable(types.ParamKeyTable()),
		storeKey:          storeKey,
		tokenPairStoreKey: tokenPairStoreKey,
		cdc:               cdc,
		cache:             NewCache(),
	}

	return k
}

func (k Keeper) GetSupplyKeeper() SupplyKeeper {
	return k.supplyKeeper
}

func (k Keeper) GetFeeCollector() string {
	return k.feeCollectorName
}

func (k Keeper) GetCDC() *codec.Codec {
	return k.cdc
}

func (k Keeper) GetTokenKeeper() TokenKeeper {
	return k.tokenKeeper
}

func (k Keeper) DeleteUserTokenPair(ctx sdk.Context, owner sdk.AccAddress, pair string) {
	store := ctx.KVStore(k.tokenPairStoreKey)
	store.Delete(types.GetUserTokenPairAddress(owner, pair))
}

// SaveTokenPair save the token pair to db
// key is base:quote
func (k Keeper) SaveTokenPair(ctx sdk.Context, tokenPair *types.TokenPair) error {
	store := ctx.KVStore(k.tokenPairStoreKey)

	var tokenPairNumber uint64
	// to load exported data from genesis file.
	if tokenPair.ID == 0 {
		tokenPairNumber = k.GetTokenPairNum(ctx)
		tokenPair.ID = tokenPairNumber + 1
	}

	tokenPairNumber = tokenPair.ID
	tokenPairNumberInByte := k.cdc.MustMarshalBinaryBare(tokenPairNumber)
	store.Set(types.TokenPairNumberKey, tokenPairNumberInByte)

	keyPair := tokenPair.BaseAssetSymbol + "_" + tokenPair.QuoteAssetSymbol
	store.Set(types.GetTokenPairAddress(keyPair), k.cdc.MustMarshalBinaryBare(tokenPair))
	store.Set(types.GetUserTokenPairAddress(tokenPair.Owner, keyPair), []byte{})

	k.cache.AddNewTokenPair(tokenPair)
	k.cache.AddTokenPair(tokenPair)
	return nil
}

// GetTokenPair return all the token pairs
func (k Keeper) GetTokenPair(ctx sdk.Context, product string) *types.TokenPair {
	var tokenPair *types.TokenPair
	//use cache
	tokenPair, ok := k.cache.GetTokenPair(product)
	if ok {
		return tokenPair
	}

	store := ctx.KVStore(k.tokenPairStoreKey)
	bytes := store.Get(types.GetTokenPairAddress(product))
	if bytes == nil {
		return nil
	}

	if k.cdc.UnmarshalBinaryBare(bytes, &tokenPair) != nil {
		ctx.Logger().Info("decoding of token pair is failed", product)
		return nil
	}
	k.cache.AddTokenPair(tokenPair)
	return tokenPair
}

// get token pair from store without cache
func (k Keeper) GetTokenPairFromStore(ctx sdk.Context, product string) *types.TokenPair {
	var tokenPair types.TokenPair
	store := ctx.KVStore(k.tokenPairStoreKey)
	bytes := store.Get(types.GetTokenPairAddress(product))
	if bytes == nil {
		return nil
	}
	if k.cdc.UnmarshalBinaryBare(bytes, &tokenPair) != nil {
		ctx.Logger().Info("decoding of token pair is failed", product)
		return nil
	}

	return &tokenPair
}

// GetTokenPairs return all the token pairs
func (k Keeper) GetTokenPairs(ctx sdk.Context) []*types.TokenPair {
	//load from cache, if not exist, load from local db
	cacheTokenPairs := k.cache.GetAllTokenPairs()
	if len(cacheTokenPairs) > 0 {
		return cacheTokenPairs
	}

	return k.GetTokenPairsFromStore(ctx)
}

// get all token pairs from store without cache
func (k Keeper) GetTokenPairsFromStore(ctx sdk.Context) (tokenPairs []*types.TokenPair) {
	store := ctx.KVStore(k.tokenPairStoreKey)
	iter := sdk.KVStorePrefixIterator(store, types.TokenPairKey)
	defer iter.Close()
	for iter.Valid() {
		var tokenPair types.TokenPair
		tokenPairBytes := iter.Value()
		k.cdc.MustUnmarshalBinaryBare(tokenPairBytes, &tokenPair)
		tokenPairs = append(tokenPairs, &tokenPair)
		iter.Next()
	}

	return tokenPairs
}

// get all token pairs from store without cache
func (k Keeper) GetUserTokenPairs(ctx sdk.Context, owner sdk.AccAddress) (tokenPairs []*types.TokenPair) {
	store := ctx.KVStore(k.tokenPairStoreKey)
	userTokenPairPrefix := types.GetUserTokenPairAddressPrefix(owner)
	prefixLen := len(userTokenPairPrefix)

	iter := sdk.KVStorePrefixIterator(store, userTokenPairPrefix)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		tokenPairName := string(key[prefixLen:])

		tokenPairs = append(tokenPairs, k.GetTokenPairFromStore(ctx, tokenPairName))
	}

	return tokenPairs
}

// DeleteTokenPairByName drop the token pair
func (k Keeper) DeleteTokenPairByName(ctx sdk.Context, owner sdk.AccAddress, product string) {
	// get store
	store := ctx.KVStore(k.tokenPairStoreKey)
	// delete the token pair from the store
	store.Delete(types.GetTokenPairAddress(product))
	// synchronize the cache
	k.cache.DeleteTokenPairByName(product)

	// remove the user-tokenpair relationship
	k.DeleteUserTokenPair(ctx, owner, product)
}

func (k Keeper) UpdateUserTokenPair(ctx sdk.Context, product string, owner, to sdk.AccAddress) {
	store := ctx.KVStore(k.tokenPairStoreKey)
	store.Delete(types.GetUserTokenPairAddress(owner, product))
	store.Set(types.GetUserTokenPairAddress(to, product), []byte{})
}

// set token pair field 'IsUnderDelisting' to be true to the store and cache
func (k Keeper) UpdateTokenPair(ctx sdk.Context, product string, tokenPair *types.TokenPair) {
	store := ctx.KVStore(k.tokenPairStoreKey)
	store.Set(types.GetTokenPairAddress(product), k.cdc.MustMarshalBinaryBare(*tokenPair))
	k.cache.AddTokenPair(tokenPair)
}

// CheckTokenPairUnderDexDelist checks if token pair is under delist. for x/order: It's not allowed to place an order about the tokenpair under dex delist
func (k Keeper) CheckTokenPairUnderDexDelist(ctx sdk.Context, product string) (isDelisting bool, err error) {
	tp := k.GetTokenPair(ctx, product)
	if tp != nil {
		isDelisting = k.GetTokenPair(ctx, product).Delisting
		err = nil
	} else {
		isDelisting = true
		err = errors.Errorf("product %s doesn't exist", product)
	}
	return isDelisting, err
}

// GetNewTokenPair return all the net token pairs
func (k Keeper) GetNewTokenPair() []*types.TokenPair {
	return k.cache.GetNewTokenPair()
}

// ResetCache resets cache
func (k Keeper) ResetCache(ctx sdk.Context) {
	k.cache.Reset()

	if len(k.cache.lockMap.Data) == 0 {
		k.cache.lockMap = k.LoadProductLocks(ctx)
	}
	//if cache data is empty, update from local db
	if k.cache.TokenPairCount() <= 0 {
		tokenPairs := k.GetTokenPairs(ctx)
		//prepare token pair cache data, we will empty cache, put db data into cache
		k.cache.PrepareTokenPairs(tokenPairs)
	}
}

// Deposit deposits amount of tokens for a product
func (k Keeper) Deposit(ctx sdk.Context, product string, from sdk.AccAddress, amount sdk.DecCoin) sdk.Error {
	tokenPair := k.GetTokenPair(ctx, product)
	if tokenPair == nil {
		return sdk.ErrUnknownRequest(fmt.Sprintf("failed to deposit beacuse non-exist product: %s", product))
	}

	if !tokenPair.Owner.Equals(from) {
		return sdk.ErrInvalidAddress(fmt.Sprintf("failed to deposit beacuse %s is not the owner of product:%s", from.String(), product))
	}

	if amount.Denom != sdk.DefaultBondDenom {
		return sdk.ErrUnknownRequest(fmt.Sprintf("failed to deposit beacuse deposits only support %s token", sdk.DefaultBondDenom))
	}

	depositCoins := amount.ToCoins()
	err := k.GetSupplyKeeper().SendCoinsFromAccountToModule(ctx, from, types.ModuleName, depositCoins)
	if err != nil {
		return sdk.ErrInsufficientCoins(fmt.Sprintf("failed to deposits beacuse  insufficient deposit coins(need %s)", depositCoins.String()))
	}

	tokenPair.Deposits = tokenPair.Deposits.Add(amount)
	k.UpdateTokenPair(ctx, product, tokenPair)
	return nil
}

// Withdraw withdraws amount of tokens from a product
func (k Keeper) Withdraw(ctx sdk.Context, product string, to sdk.AccAddress, amount sdk.DecCoin) sdk.Error {
	tokenPair := k.GetTokenPair(ctx, product)
	if tokenPair == nil {
		return sdk.ErrUnknownRequest(fmt.Sprintf("failed to withdraws beacuse non-exist product: %s", product))
	}

	if !tokenPair.Owner.Equals(to) {
		return sdk.ErrInvalidAddress(fmt.Sprintf("failed to withdraws beacuse %s is not the owner of product:%s", to.String(), product))
	}

	if amount.Denom != sdk.DefaultBondDenom {
		return sdk.ErrUnknownRequest(fmt.Sprintf("failed to withdraws beacuse deposits only support %s token", sdk.DefaultBondDenom))
	}

	if tokenPair.Deposits.IsLT(amount) {
		return sdk.ErrInsufficientCoins(fmt.Sprintf("failed to withdraws beacuse deposits:%s is less than withdraw:%s", tokenPair.Deposits.String(), amount.String()))
	}

	completeTime := ctx.BlockHeader().Time.Add(k.GetParams(ctx).WithdrawPeriod)
	// add withdraw info to store
	withdrawInfo, ok := k.GetWithdrawInfo(ctx, to)
	if !ok {
		withdrawInfo = types.WithdrawInfo{
			Owner:        to,
			Deposits:     amount,
			CompleteTime: completeTime,
		}
	} else {
		k.DeleteWithdrawCompleteTimeAddress(ctx, withdrawInfo.CompleteTime, to)
		withdrawInfo.Deposits = withdrawInfo.Deposits.Add(amount)
		withdrawInfo.CompleteTime = completeTime
	}
	k.SetWithdrawInfo(ctx, withdrawInfo)
	k.SetWithdrawCompleteTimeAddress(ctx, completeTime, to)

	// update token pair
	tokenPair.Deposits = tokenPair.Deposits.Sub(amount)
	k.UpdateTokenPair(ctx, product, tokenPair)
	return nil
}

// GetTokenPairsOrdered returns token pairs ordered by product
func (k Keeper) GetTokenPairsOrdered(ctx sdk.Context) types.TokenPairs {
	var result types.TokenPairs
	tokenPairs := k.GetTokenPairs(ctx)
	for _, tp := range tokenPairs {
		result = append(result, tp)
	}
	sort.Sort(result)
	return result
}

// SortProducts sorts products
func (k Keeper) SortProducts(ctx sdk.Context, products []string) {
	tokenPairs := make(types.TokenPairs, 0, len(products))
	for _, product := range products {
		tokenPair := k.GetTokenPair(ctx, product)
		if tokenPair != nil {
			tokenPairs = append(tokenPairs, tokenPair)
		}
	}
	sort.Sort(tokenPairs)

	for i, tokenPair := range tokenPairs {
		products[i] = fmt.Sprintf("%s_%s", tokenPair.BaseAssetSymbol, tokenPair.QuoteAssetSymbol)
	}
}

// GetParams gets inflation params from the global param store
func (k Keeper) GetParams(ctx sdk.Context) (params types.Params) {
	k.GetParamSubspace().GetParamSet(ctx, &params)
	return params
}

// SetParams sets inflation params from the global param store
func (k Keeper) SetParams(ctx sdk.Context, params types.Params) {
	k.GetParamSubspace().SetParamSet(ctx, &params)
}

// GetParamSubspace returns paramSubspace
func (k Keeper) GetParamSubspace() params.Subspace {
	return k.paramSubspace
}

// TransferOwnership transfers ownership of product
func (k Keeper) TransferOwnership(ctx sdk.Context, product string, from sdk.AccAddress, to sdk.AccAddress) sdk.Error {
	tokenPair := k.GetTokenPair(ctx, product)
	if tokenPair == nil {
		return sdk.ErrUnknownRequest(fmt.Sprintf("non-exist product: %s", product))
	}

	if !tokenPair.Owner.Equals(from) {
		return sdk.ErrUnauthorized(fmt.Sprintf("%s is not the owner of product(%s)", from.String(), product))
	}

	// Withdraw
	if tokenPair.Deposits.IsPositive() {
		if err := k.Withdraw(ctx, product, from, tokenPair.Deposits); err != nil {
			return sdk.ErrInternal(fmt.Sprintf("withdraw deposits:%s error:%s", tokenPair.Deposits.String(), err.Error()))
		}
	}

	// transfer ownership
	tokenPair.Owner = to
	tokenPair.Deposits = types.DefaultTokenPairDeposit
	k.UpdateTokenPair(ctx, product, tokenPair)
	k.UpdateUserTokenPair(ctx, product, from, to)

	return nil
}

// GetWithdrawInfo returns withdraw info binding the addr
func (k Keeper) GetWithdrawInfo(ctx sdk.Context, addr sdk.AccAddress) (withdrawInfo types.WithdrawInfo, ok bool) {
	bytes := ctx.KVStore(k.storeKey).Get(types.GetWithdrawAddressKey(addr))
	if bytes == nil {
		return
	}

	k.cdc.MustUnmarshalBinaryLengthPrefixed(bytes, &withdrawInfo)
	return withdrawInfo, true
}

// SetWithdrawInfo set withdraw address key with withdraw info
func (k Keeper) SetWithdrawInfo(ctx sdk.Context, withdrawInfo types.WithdrawInfo) {
	key := types.GetWithdrawAddressKey(withdrawInfo.Owner)
	bytes := k.cdc.MustMarshalBinaryLengthPrefixed(withdrawInfo)
	ctx.KVStore(k.storeKey).Set(key, bytes)
}

func (k Keeper) deleteWithdrawInfo(ctx sdk.Context, addr sdk.AccAddress) {
	ctx.KVStore(k.storeKey).Delete(types.GetWithdrawAddressKey(addr))
}

func (k Keeper) withdrawTimeKeyIterator(ctx sdk.Context, endTime time.Time) sdk.Iterator {
	store := ctx.KVStore(k.storeKey)
	key := types.GetWithdrawTimeKey(endTime)
	return store.Iterator(types.PrefixWithdrawTimeKey, sdk.PrefixEndBytes(key))
}

// SetWithdrawCompleteTimeAddress sets withdraw time key with empty []byte{} value
func (k Keeper) SetWithdrawCompleteTimeAddress(ctx sdk.Context, completeTime time.Time, addr sdk.AccAddress) {
	ctx.KVStore(k.storeKey).Set(types.GetWithdrawTimeAddressKey(completeTime, addr), []byte{})
}

// DeleteWithdrawCompleteTimeAddress deletes withdraw time key
func (k Keeper) DeleteWithdrawCompleteTimeAddress(ctx sdk.Context, timestamp time.Time, delAddr sdk.AccAddress) {
	ctx.KVStore(k.storeKey).Delete(types.GetWithdrawTimeAddressKey(timestamp, delAddr))
}

// IterateWithdrawInfo iterates withdraw address key， and returns withdraw info
func (k Keeper) IterateWithdrawInfo(ctx sdk.Context, fn func(index int64, withdrawInfo types.WithdrawInfo) (stop bool)) {
	store := ctx.KVStore(k.storeKey)
	iterator := sdk.KVStorePrefixIterator(store, types.PrefixWithdrawAddressKey)
	defer iterator.Close()

	for i := int64(0); iterator.Valid(); iterator.Next() {
		var withdrawInfo types.WithdrawInfo
		k.cdc.MustUnmarshalBinaryLengthPrefixed(iterator.Value(), &withdrawInfo)
		if stop := fn(i, withdrawInfo); stop {
			break
		}
		i++
	}
}

// IterateWithdrawAddress itreate withdraw time keys, and returns address
func (k Keeper) IterateWithdrawAddress(ctx sdk.Context, currentTime time.Time,
	fn func(index int64, key []byte) (stop bool)) {
	// iterate for all keys of (time+delAddr) from time 0 until the current time
	timeKeyIterator := k.withdrawTimeKeyIterator(ctx, currentTime)
	defer timeKeyIterator.Close()

	for i := int64(0); timeKeyIterator.Valid(); timeKeyIterator.Next() {
		key := timeKeyIterator.Key()
		if stop := fn(i, key); stop {
			break
		}
		i++
	}
}

// CompleteWithdraw completes withdrawing of addr
func (k Keeper) CompleteWithdraw(ctx sdk.Context, addr sdk.AccAddress) error {
	withdrawInfo, ok := k.GetWithdrawInfo(ctx, addr)
	if !ok {
		return sdk.ErrInvalidAddress(fmt.Sprintf("there is no withdrawing for address%s", addr.String()))
	}
	withdrawCoins := withdrawInfo.Deposits.ToCoins()
	err := k.GetSupplyKeeper().SendCoinsFromModuleToAccount(ctx, types.ModuleName, withdrawInfo.Owner, withdrawCoins)
	if err != nil {
		return sdk.ErrInsufficientCoins(fmt.Sprintf("withdraw error: %s, insufficient deposit coins(need %s)",
			err.Error(), withdrawCoins.String()))
	}
	k.deleteWithdrawInfo(ctx, addr)
	return nil
}

// IsTokenPairChanged returns true if token pair changed during lifetime of the block
func (k Keeper) IsTokenPairChanged() bool {
	return k.cache.tokenPairChanged
}

// SetGovKeeper sets keeper of gov
func (k *Keeper) SetGovKeeper(gk GovKeeper) {
	k.govKeeper = gk
}

func (k Keeper) GetTokenPairNum(ctx sdk.Context) (tokenPairNumber uint64) {
	store := ctx.KVStore(k.tokenPairStoreKey)
	b := store.Get(types.TokenPairNumberKey)
	if b != nil {
		k.cdc.MustUnmarshalBinaryBare(b, &tokenPairNumber)
	}
	return
}
