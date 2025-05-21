package app

import (
	"encoding/json"
	"io"
	"os"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/grpc/tmservice"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/bank"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/capability"
	capabilitykeeper "github.com/cosmos/cosmos-sdk/x/capability/keeper"
	capabilitytypes "github.com/cosmos/cosmos-sdk/x/capability/types"
	"github.com/cosmos/cosmos-sdk/x/params"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/tendermint/tendermint/libs/log"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	"github.com/pandapls/cosmos-bitcoin-chain/x/bitcoin"
	bitcoinkeeper "github.com/pandapls/cosmos-bitcoin-chain/x/bitcoin/keeper"
	bitcointypes "github.com/pandapls/cosmos-bitcoin-chain/x/bitcoin/types"
)

const (
	AppName = "btcd"
)

var (
	DefaultNodeHome string
)

func init() {
	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}

	DefaultNodeHome = userHomeDir + "/.btcd"
}

var (
	// ModuleBasics defines the module BasicManager is in charge of setting up basic,
	// non-dependant module elements, such as codec registration
	ModuleBasics = module.NewBasicManager(
		auth.AppModuleBasic{},
		bank.AppModuleBasic{},
		params.AppModuleBasic{},
		capability.AppModuleBasic{},
		staking.AppModuleBasic{},
		bitcoin.AppModuleBasic{},
	)
)

// App extends baseapp.BaseApp
type App struct {
	*baseapp.BaseApp
	cdc               *codec.LegacyAmino
	appCodec          codec.Codec
	interfaceRegistry types.InterfaceRegistry

	invCheckPeriod uint

	// keys to access the substores
	keys    map[string]*sdk.KVStoreKey
	tkeys   map[string]*sdk.TransientStoreKey
	memKeys map[string]*sdk.MemoryStoreKey

	// keepers
	AccountKeeper    authkeeper.AccountKeeper
	BankKeeper       bankkeeper.Keeper
	CapabilityKeeper *capabilitykeeper.Keeper
	StakingKeeper    stakingkeeper.Keeper
	ParamsKeeper     paramskeeper.Keeper
	BitcoinKeeper    bitcoinkeeper.Keeper

	// the module manager
	mm *module.Manager
}

// NewBitcoinApp is the main constructor function for the application
func NewBitcoinApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	skipUpgradeHeights map[int64]bool,
	homePath string,
	invCheckPeriod uint,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *App {
	// Initialize the application
	encodingConfig := MakeEncodingConfig()
	appCodec, legacyAmino := encodingConfig.Marshaler, encodingConfig.Amino
	interfaceRegistry := encodingConfig.InterfaceRegistry

	bApp := baseapp.NewBaseApp(AppName, logger, db, authtx.DefaultTxDecoder(legacyAmino), baseAppOptions...)
	bApp.SetCommitMultiStoreTracer(traceStore)
	bApp.SetVersion("1.0")
	bApp.SetInterfaceRegistry(interfaceRegistry)

	// Create app instance
	app := &App{
		BaseApp:           bApp,
		cdc:               legacyAmino,
		appCodec:          appCodec,
		interfaceRegistry: interfaceRegistry,
		invCheckPeriod:    invCheckPeriod,
		keys:              sdk.NewKVStoreKeys(
			authtypes.StoreKey, banktypes.StoreKey, stakingtypes.StoreKey,
			paramstypes.StoreKey, capabilitytypes.StoreKey, bitcointypes.StoreKey,
		),
		tkeys:             sdk.NewTransientStoreKeys(paramstypes.TStoreKey),
		memKeys:           sdk.NewMemoryStoreKeys(capabilitytypes.MemStoreKey),
	}

	// Set up param keeper
	app.ParamsKeeper = paramskeeper.NewKeeper(
		appCodec, legacyAmino, app.keys[paramstypes.StoreKey], app.tkeys[paramstypes.TStoreKey],
	)

	// Set up account keeper
	app.AccountKeeper = authkeeper.NewAccountKeeper(
		appCodec, app.keys[authtypes.StoreKey], authtypes.ProtoBaseAccount, nil, sdk.Bech32MainPrefix,
	)

	// Set up bank keeper
	app.BankKeeper = bankkeeper.NewBaseKeeper(
		appCodec, app.keys[banktypes.StoreKey], app.AccountKeeper, nil, nil,
	)

	// Set up capability keeper
	scopedCapabilityKeeper := capabilitykeeper.NewScopedKeeper(
		appCodec, app.keys[capabilitytypes.StoreKey], app.memKeys[capabilitytypes.MemStoreKey],
	)
	app.CapabilityKeeper = capabilitykeeper.NewKeeper(
		appCodec, app.keys[capabilitytypes.StoreKey], app.memKeys[capabilitytypes.MemStoreKey],
	)

	// Set up staking keeper
	app.StakingKeeper = stakingkeeper.NewKeeper(
		appCodec, app.keys[stakingtypes.StoreKey], app.AccountKeeper, app.BankKeeper, nil,
	)

	// Set up bitcoin keeper
	app.BitcoinKeeper = bitcoinkeeper.NewKeeper(
		appCodec, app.keys[bitcointypes.StoreKey], app.BankKeeper, app.AccountKeeper,
	)

	// Register modules
	app.mm = module.NewManager(
		auth.NewAppModule(appCodec, app.AccountKeeper, nil),
		bank.NewAppModule(appCodec, app.BankKeeper, app.AccountKeeper),
		capability.NewAppModule(appCodec, *app.CapabilityKeeper),
		params.NewAppModule(app.ParamsKeeper),
		staking.NewAppModule(appCodec, app.StakingKeeper, app.AccountKeeper, app.BankKeeper),
		bitcoin.NewAppModule(app.BitcoinKeeper, app.BankKeeper),
	)

	// Set order of execution
	app.mm.SetOrderBeginBlockers(
		capabilitytypes.ModuleName,
		authtypes.ModuleName,
		banktypes.ModuleName,
		stakingtypes.ModuleName,
		bitcointypes.ModuleName,
	)

	app.mm.SetOrderEndBlockers(
		stakingtypes.ModuleName,
		bitcointypes.ModuleName,
	)

	// Set order of initialization
	app.mm.SetOrderInitGenesis(
		capabilitytypes.ModuleName,
		authtypes.ModuleName,
		banktypes.ModuleName,
		stakingtypes.ModuleName,
		bitcointypes.ModuleName,
	)

	// Initialize stores
	app.MountKVStores(app.keys)
	app.MountTransientStores(app.tkeys)
	app.MountMemoryStores(app.memKeys)

	// Initialize the application
	if loadLatest {
		if err := app.LoadLatestVersion(); err != nil {
			panic(err)
		}
	}

	return app
}

// MakeEncodingConfig creates encoding configuration used by the app
func MakeEncodingConfig() EncodingConfig {
	amino := codec.NewLegacyAmino()
	interfaceRegistry := types.NewInterfaceRegistry()
	marshaler := codec.NewProtoCodec(interfaceRegistry)
	txCfg := authtx.NewTxConfig(marshaler, authtx.DefaultSignModes)

	return EncodingConfig{
		InterfaceRegistry: interfaceRegistry,
		Marshaler:         marshaler,
		TxConfig:          txCfg,
		Amino:             amino,
	}
}

// EncodingConfig specifies the concrete encoding types to use for a given app.
type EncodingConfig struct {
	InterfaceRegistry types.InterfaceRegistry
	Marshaler         codec.Codec
	TxConfig          client.TxConfig
	Amino             *codec.LegacyAmino
}

// Name returns the name of the App
func (app *App) Name() string { return app.BaseApp.Name() }

// BeginBlocker calls the BeginBlocker of all modules with the BeginBlock context.
func (app *App) BeginBlocker(ctx sdk.Context, req tmproto.RequestBeginBlock) tmproto.ResponseBeginBlock {
	return app.mm.BeginBlock(ctx, req)
}

// EndBlocker calls the EndBlocker of all modules with the EndBlock context.
func (app *App) EndBlocker(ctx sdk.Context, req tmproto.RequestEndBlock) tmproto.ResponseEndBlock {
	return app.mm.EndBlock(ctx, req)
}

// InitChainer initializes the chain from the genesis state.
func (app *App) InitChainer(ctx sdk.Context, req tmproto.RequestInitChain) tmproto.ResponseInitChain {
	var genesisState map[string]json.RawMessage
	app.cdc.MustUnmarshalJSON(req.AppStateBytes, &genesisState)
	return app.mm.InitGenesis(ctx, app.appCodec, genesisState)
}

// LoadHeight loads a particular height
func (app *App) LoadHeight(height int64) error {
	return app.LoadVersion(height)
}

// RegisterGRPCServer registers gRPC services
func (app *App) RegisterGRPCServer(server grpc.Server) {
	tmservice.RegisterServiceServer(server, tmservice.NewQueryServer(app.BaseApp))
}

// RegisterAPIRoutes registers all application module routes with the provided API server.
func (app *App) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	clientCtx := apiSvr.ClientCtx
	authtx.RegisterGRPCGatewayRoutes(clientCtx, apiSvr.GRPCGatewayRouter)
	// Register other module routes here
}

// RegisterTxService implements the Application.RegisterTxService method.
func (app *App) RegisterTxService(clientCtx client.Context) {
	authtx.RegisterTxService(app.BaseApp.GRPCQueryRouter(), clientCtx, app.BaseApp.Simulate, app.interfaceRegistry)
}

// RegisterTendermintService implements the Application.RegisterTendermintService method.
func (app *App) RegisterTendermintService(clientCtx client.Context) {
	tmservice.RegisterTendermintService(app.BaseApp.GRPCQueryRouter(), clientCtx, app.interfaceRegistry)
}

// ModuleAccountAddrs returns all the app's module account addresses.
func (app *App) ModuleAccountAddrs() map[string]bool {
	modAccAddrs := make(map[string]bool)
	// Add module accounts here
	return modAccAddrs
}

// GetModuleManager returns the app's module manager
func (app *App) GetModuleManager() *module.Manager {
	return app.mm
}
