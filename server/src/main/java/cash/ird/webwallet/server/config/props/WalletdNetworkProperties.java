package cash.ird.webwallet.server.config.props;

import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;

import java.util.List;

@Data
@Configuration
@ConfigurationProperties(prefix = "walletd.network")
public class WalletdNetworkProperties {

    private String name;

}
