package wallet

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/iridiumdev/webwallet-core/config"
	"github.com/iridiumdev/webwallet-core/iridium"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/mgo.v2/bson"
	"net"
)

type Service interface {
	CreateWallet(dto CreateDTO, userId string) (*DetailedWallet, error)
	ImportWallet(dto ImportDTO, userId string) (*DetailedWallet, error)

	GetWallets(userId string) ([]*Wallet, error)
	GetWallet(walletId string, userId string) (*DetailedWallet, error)

	StartWallet(walletId string, password string, userId string) (*DetailedWallet, error)
	StopWallet(walletId string, userId string) (*Wallet, error)

	FetchDetails(wallet *LoadedWallet, rpc iridium.WalletdRPC) (*DetailedWallet, error)
	NewWalletdClient(walletId string) (iridium.WalletdRPC, error)
}

type containerStatus string

const (
	DOCKER_EXITED  containerStatus = "exited"
	DOCKER_RUNNING containerStatus = "running"
)

var (
	ErrWalletNotFound   = errors.New("wallet not found")
	ErrWalletNotRunning = errors.New("wallet not running")

	ErrWalletAlreadyRunning = errors.New("wallet already running")

	ErrCouldNotStopWallet  = errors.New("wallet could not be stopped")
	ErrCouldNotStartWallet = errors.New("wallet could not be started")
	ErrCouldNotSaveWallet  = errors.New("wallet could not be saved")
	ErrCouldNotKillWallet  = errors.New("wallet could not be killed")
)

var service Service

type serviceImpl struct {
	dockerClient *client.Client
}

func InitService(dockerClient *client.Client) Service {
	service = &serviceImpl{dockerClient: dockerClient}
	return service
}

func (s *serviceImpl) CreateWallet(dto CreateDTO, userId string) (*DetailedWallet, error) {

	wallet := &Wallet{
		Id:    bson.NewObjectId(),
		Name:  dto.Name,
		Owner: bson.ObjectIdHex(userId),
	}

	if err := s.createNewVolume(wallet); err != nil {
		return nil, err
	}
	if _, err := s.instantiateContainer(wallet, dto.Password); err != nil {
		return nil, err
	}

	walletd, err := s.NewWalletdClient(wallet.Id.Hex())
	if err != nil {
		if err := s.killWallet(wallet.Id.Hex(), userId); err != nil {
			return nil, err
		}
		return nil, err
	}

	addresses, err := walletd.GetAddresses()
	if err != nil {
		return nil, err
	}

	if len(addresses) > 0 {
		wallet.Address = addresses[0]
	} else {
		return nil, errors.New("could not fetch wallet address!")
	}

	err = store.InsertWallet(wallet)

	lWallet := &LoadedWallet{Wallet: wallet}

	dWallet, err := s.FetchDetails(lWallet, walletd)

	if err != nil {
		log.Errorf("Could not fetch wallet %s details!", walletd)
		if err := s.killWallet(dWallet.Id.Hex(), userId); err != nil {
			return nil, err
		}
	} else {
		statusWatcher.AddWallet(dWallet.LoadedWallet)
	}

	return dWallet, err
}

func (s *serviceImpl) ImportWallet(dto ImportDTO, userId string) (*DetailedWallet, error) {

	wallet := &Wallet{
		Id:    bson.NewObjectId(),
		Name:  dto.Name,
		Owner: bson.ObjectIdHex(userId),
	}

	if err := s.createNewVolume(wallet); err != nil {
		return nil, err
	}
	if _, err := s.instantiateContainer(wallet, dto.Password); err != nil {
		return nil, err
	}

	walletd, err := s.NewWalletdClient(wallet.Id.Hex())
	if err != nil {
		if err := s.killWallet(wallet.Id.Hex(), userId); err != nil {
			return nil, err
		}
		return nil, err
	}

	if err := walletd.Reset(dto.ViewSecretKey); err != nil {
		return nil, err
	}
	address, err := walletd.CreateAddress(dto.SpendSecretKey)
	if err != nil {
		return nil, err
	}

	err = walletd.Save()
	if err != nil {
		return nil, err
	}

	wallet.Address = address
	err = store.InsertWallet(wallet)

	lWallet := &LoadedWallet{Wallet: wallet}

	dWallet, err := s.FetchDetails(lWallet, walletd)

	if err != nil {
		log.Errorf("Could not fetch wallet %s details!", walletd)
		if err := s.killWallet(dWallet.Id.Hex(), userId); err != nil {
			return nil, err
		}
	} else {
		statusWatcher.AddWallet(dWallet.LoadedWallet)
	}

	return dWallet, err
}

func (s *serviceImpl) GetWallets(userId string) ([]*Wallet, error) {
	wallets, e := store.FindWalletsByOwner(bson.ObjectIdHex(userId))
	for k, wallet := range wallets {
		if _, err := s.checkContainerRunning(wallet); err != nil {
			wallets[k].Status = STOPPED
		} else {
			wallets[k].Status = RUNNING
		}
	}
	return wallets, e
}

func (s *serviceImpl) GetWallet(walletId string, userId string) (*DetailedWallet, error) {

	wallet, err := store.FindWalletByOwner(bson.ObjectIdHex(walletId), bson.ObjectIdHex(userId))
	if err != nil || wallet == nil {
		log.Warnf("Could not find wallet %s for user %s, err: %s", walletId, userId, err.Error())
		return nil, ErrWalletNotFound
	}

	lWallet := &LoadedWallet{Wallet: wallet}
	dWallet := &DetailedWallet{LoadedWallet: lWallet}

	if _, err := s.checkContainerRunning(wallet); err != nil {
		return nil, err
	}

	walletd, err := s.NewWalletdClient(wallet.Id.Hex())
	if err != nil {
		dWallet.Status = ERROR
		return dWallet, err
	}

	dWallet, err = s.FetchDetails(lWallet, walletd)

	return dWallet, err
}

func (s *serviceImpl) StartWallet(walletId string, password string, userId string) (*DetailedWallet, error) {

	wallet, err := store.FindWalletByOwner(bson.ObjectIdHex(walletId), bson.ObjectIdHex(userId))
	if err != nil || wallet == nil {
		log.Warnf("Could not find wallet %s for user %s, err: %s", walletId, userId, err.Error())
		return nil, ErrWalletNotFound
	}

	walletContainer, _ := s.checkContainerRunning(wallet)
	if walletContainer != nil {
		return nil, ErrWalletAlreadyRunning
	}

	loadedWallet, err := s.instantiateContainer(wallet, password)
	if err != nil {
		log.Debugf("Could not start wallet %s due to: %s", walletId, err.Error())
		return nil, ErrCouldNotStartWallet
	}

	walletd, err := s.NewWalletdClient(walletId)
	if err != nil {
		log.Debugf("Could not start wallet %s due to: %s", walletId, err.Error())
		if err := s.killWallet(walletId, userId); err != nil {
			return nil, err
		}
		return nil, ErrCouldNotStartWallet
	}

	detailedWallet, err := s.FetchDetails(loadedWallet, walletd)
	if err != nil {
		log.Errorf("Could not fetch wallet %s details!", walletd)
		if err := s.killWallet(walletId, userId); err != nil {
			return nil, err
		}
	} else {
		statusWatcher.AddWallet(detailedWallet.LoadedWallet)
	}

	return detailedWallet, err
}

func (s *serviceImpl) StopWallet(walletId string, userId string) (*Wallet, error) {

	ctx := context.Background()

	wallet, err := store.FindWalletByOwner(bson.ObjectIdHex(walletId), bson.ObjectIdHex(userId))
	if err != nil || wallet == nil {
		log.Warnf("Could not find wallet %s for user %s, err: %s", walletId, userId, err.Error())
		return nil, ErrWalletNotFound
	}

	walletContainer, err := s.checkContainerRunning(wallet)
	if err != nil {
		if err == ErrWalletNotRunning {
			wallet.Status = STOPPED
			return wallet, nil
		} else {
			log.Errorf("Could not stop wallet %s due to: %s", walletId, err.Error())
			return nil, ErrCouldNotStopWallet
		}
	}
	wallet.Status = RUNNING

	walletd, err := s.NewWalletdClient(walletId)
	if err != nil {
		log.Debugf("Could not save wallet %s due to: %s", walletId, err.Error())
		if err := s.killWallet(walletId, userId); err != nil {
			return nil, err
		}
		return nil, ErrCouldNotSaveWallet
	}

	if err := walletd.Save(); err != nil {
		log.Warnf("Could not save to wallet file %s for user %s, err: %s", walletId, userId, err.Error())
		return nil, ErrCouldNotSaveWallet
	}

	err = s.dockerClient.ContainerRemove(ctx, walletContainer.ID, types.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil {
		log.Errorf("Could not stop wallet %s due to: %s", walletId, err.Error())
		return nil, ErrCouldNotStopWallet
	}

	wallet.Status = STOPPED

	statusWatcher.RemoveWallet(wallet)

	return wallet, nil
}

func (s *serviceImpl) killWallet(walletId string, userId string) error {

	ctx := context.Background()

	wallet, err := store.FindWalletByOwner(bson.ObjectIdHex(walletId), bson.ObjectIdHex(userId))
	if err != nil || wallet == nil {
		log.Warnf("Could not find wallet %s for user %s, err: %s", walletId, userId, err.Error())
		return ErrWalletNotFound
	}

	statusWatcher.RemoveWallet(wallet)

	cList, err := s.getContainer(wallet, DOCKER_RUNNING)
	if err != nil {
		log.Error(err)
	}
	if len(cList) == 0 {
		cList, err = s.getContainer(wallet, DOCKER_EXITED)
		if err != nil {
			log.Error(err)
			return err
		}
	}

	if len(cList) == 0 {
		log.Errorf("Could not find container for wallet %s!", walletId)
		return ErrCouldNotKillWallet
	}

	walletContainer := cList[0]

	err = s.dockerClient.ContainerRemove(ctx, walletContainer.ID, types.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil {
		log.Errorf("Could not stop wallet %s due to: %s", walletId, err.Error())
		return ErrCouldNotStopWallet
	}

	return nil
}

func (s *serviceImpl) checkContainerRunning(wallet *Wallet) (*types.Container, error) {
	cList, err := s.getContainer(wallet, DOCKER_RUNNING)

	if err != nil {
		log.Errorf("Could not check status of wallet %s: %s", wallet.Id.Hex(), err.Error())
		return nil, ErrWalletNotRunning
	}

	if len(cList) == 0 {
		return nil, ErrWalletNotRunning
	} else {
		return &cList[0], nil
	}
}

func (s *serviceImpl) getContainer(wallet *Wallet, status containerStatus) ([]types.Container, error) {
	ctx := context.Background()

	listFilters := filters.NewArgs()
	listFilters.Add("name", wallet.Id.Hex())
	listFilters.Add("status", string(status))

	for k, v := range config.Get().Webwallet.Satellite.Labels {
		listFilters.Add("label", fmt.Sprintf("%s=%s", k, v))
	}

	return s.dockerClient.ContainerList(ctx, types.ContainerListOptions{
		Limit:   1,
		Filters: listFilters,
	})
}

func (s *serviceImpl) createNewVolume(wallet *Wallet) error {
	ctx := context.Background()

	log.Infof("Creating new volume for wallet with id '%s'", wallet.Id.Hex())
	_, err := s.dockerClient.VolumeCreate(ctx, volume.VolumesCreateBody{
		Name:   fmt.Sprintf("%s.wallet", wallet.Id.Hex()),
		Labels: config.Get().Webwallet.Satellite.Labels,
	})
	if err != nil {
		return err
	}
	log.Debugf("Created new volume for wallet with id '%s' successfully!", wallet.Id.Hex())
	return nil
}

func (s *serviceImpl) instantiateContainer(wallet *Wallet, password string) (*LoadedWallet, error) {
	ctx := context.Background()

	command := append(config.Get().Webwallet.Satellite.Command,
		fmt.Sprintf("--container-password=%s", password),
	)

	volumeName := fmt.Sprintf("%s.wallet", wallet.Id.Hex())
	_, err := s.dockerClient.ContainerCreate(ctx, &container.Config{
		Image:  config.Get().Webwallet.Satellite.Image,
		Cmd:    command,
		Labels: config.Get().Webwallet.Satellite.Labels,
	}, &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Source: volumeName,
				Target: "/data",
			},
		},
	}, nil, wallet.Id.Hex())

	if err != nil {
		return nil, err
	}

	log.Infof("Attaching network '%s' to container for wallet with id '%s'", config.Get().Webwallet.Network, wallet.Id.Hex())

	if err := s.dockerClient.NetworkConnect(ctx, config.Get().Webwallet.Network, wallet.Id.Hex(), nil); err != nil {
		return nil, err
	}

	log.Infof("Starting container for wallet with id '%s'", wallet.Id.Hex())

	if err := s.dockerClient.ContainerStart(ctx, wallet.Id.Hex(), types.ContainerStartOptions{}); err != nil {
		return nil, err
	}

	log.Debugf("Started container for wallet with id '%s'", wallet.Id.Hex())

	loadedWallet := &LoadedWallet{
		Wallet: wallet,
	}

	return loadedWallet, nil
}

func (s *serviceImpl) FetchDetails(wallet *LoadedWallet, rpc iridium.WalletdRPC) (*DetailedWallet, error) {
	dWallet := &DetailedWallet{LoadedWallet: wallet}
	dWallet.Status = RUNNING

	sRes, err := rpc.GetStatus()
	if err != nil {
		wallet.Status = ERROR
		return dWallet, err
	}

	dWallet.BlockHeight = BlockHeight{
		Current: sRes.BlockCount,
		Top:     sRes.KnownBlockCount,
	}
	dWallet.PeerCount = sRes.PeerCount

	bRes, err := rpc.GetBalance()
	if err != nil {
		wallet.Status = ERROR
		return dWallet, err
	}

	dWallet.Balance = Balance{
		Total:  bRes.AvailableBalance,
		Locked: bRes.LockedAmount,
	}

	return dWallet, nil
}

func (s *serviceImpl) NewWalletdClient(walletId string) (iridium.WalletdRPC, error) {
	containerEndpoint, err := s.resolveContainerEndpoint(walletId)
	if err != nil {
		return nil, err
	}
	rpcHost := net.JoinHostPort(containerEndpoint, config.Get().Webwallet.Satellite.RpcPort)
	rpcAddress := fmt.Sprintf("http://%s/json_rpc", rpcHost)

	return iridium.Walletd(rpcAddress)
}

func (s *serviceImpl) resolveContainerEndpoint(containerId string) (string, error) {
	ctx := context.Background()

	if config.Get().Webwallet.InternalResolver {
		return containerId, nil
	} else {
		log.Debugf("Using 'ip' resolver to get the satellites endpoint address")
		inspect, err := s.dockerClient.ContainerInspect(ctx, containerId)
		if err != nil {
			return "", err
		}

		return inspect.NetworkSettings.Networks[config.Get().Webwallet.Network].IPAddress, nil
	}

}
